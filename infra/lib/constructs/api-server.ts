import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as ecs from "aws-cdk-lib/aws-ecs";
import * as ecr from "aws-cdk-lib/aws-ecr";
import * as elbv2 from "aws-cdk-lib/aws-elasticloadbalancingv2";
import * as iam from "aws-cdk-lib/aws-iam";
import * as logs from "aws-cdk-lib/aws-logs";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import { Construct } from "constructs";

export interface ApiServerProps {
  env: string;
  vpc: ec2.IVpc;
  cluster: ecs.ICluster;
  taskRole: iam.IRole;
  executionRole: iam.IRole;
  dbProxyEndpoint: string;
  ingestTaskSecret: secretsmanager.ISecret; // shared DB credential (onefintech/{env}/db/ingest-task)
}

/**
 * ApiServerConstruct — One Fintech Card & Member API server.
 *
 * Architecture:
 *   ALB (internal, private subnets, port 80) → ECS Fargate service (private, port 8080)
 *
 * Internal ALB: accessible from within the VPC (Grafana, other services, VPN).
 * Not exposed to the public internet — no public subnets in target group.
 *
 * Swagger UI: available at http://{alb-dns}/docs/
 * Health check: GET /healthz → 200 {"status":"ok"}
 *
 * FIS mode:
 *   DEV: -fis-mock=true (in-memory mock, no FIS credentials required)
 *   TST/PRD: -fis-mock=false (real FIS adapter — pending John Stevens credentials)
 *
 * Cost (DEV):
 *   Fargate: 0.25 vCPU / 512MB = ~$6/month
 *   Internal ALB: ~$18/month
 *   Total: ~$24/month — shares cluster with ingest-task (no extra cluster cost)
 */
export class ApiServerConstruct extends Construct {
  /** DNS name of the internal ALB. Use to access Swagger UI: http://{albDns}/docs/ */
  public readonly albDns: string;
  public readonly service: ecs.FargateService;
  public readonly repository: ecr.Repository;

  constructor(scope: Construct, id: string, props: ApiServerProps) {
    super(scope, id);

    const { env, vpc, cluster, taskRole, executionRole } = props;

    // ── ECR repository ────────────────────────────────────────────────────
    this.repository = new ecr.Repository(this, "Repository", {
      repositoryName: `onefintech-${env}/api-server`,
      imageScanOnPush: true,
      removalPolicy: env === "dev" ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN,
      lifecycleRules: [{ maxImageCount: 10, rulePriority: 1 }],
    });

    // ── CloudWatch log group (6-year HIPAA retention) ─────────────────────
    const logGroup = new logs.LogGroup(this, "LogGroup", {
      logGroupName: `/onefintech/${env}/api-server`,
      retention: logs.RetentionDays.SIX_YEARS,
      removalPolicy: env === "dev" ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN,
    });

    // ── Task definition ───────────────────────────────────────────────────
    const taskDef = new ecs.FargateTaskDefinition(this, "TaskDef", {
      family: `onefintech-${env}-api-server`,
      cpu: 256,          // 0.25 vCPU — long-lived service; right-size before UAT
      memoryLimitMiB: 512,
      taskRole,
      executionRole,
    });

    taskDef.addContainer("api-server", {
      image: ecs.ContainerImage.fromEcrRepository(this.repository, "latest"),
      command: [
        "/api-server",
        `-addr=:8080`,
        `-fis-mock=${env === "dev" ? "true" : "false"}`,
        `-fis-subprog-id=26071`,
      ],
      environment: {
        PIPELINE_ENV: env.toUpperCase(),
        AWS_REGION:   cdk.Stack.of(this).region,
        DB_HOST:      props.dbProxyEndpoint,
        DB_NAME:      "onefintech",
        DB_USER:      "ingest_task",
        DB_SSL:       "require",
      },
      secrets: {
        DB_PASSWORD: ecs.Secret.fromSecretsManager(props.ingestTaskSecret, "password"),
      },
      portMappings: [{ containerPort: 8080, protocol: ecs.Protocol.TCP }],
      logging: ecs.LogDrivers.awsLogs({ streamPrefix: "api-server", logGroup }),
      essential: true,
      // No container health check — scratch image has no shell or wget.
      // ALB target group health check (GET /healthz → 200) is the liveness gate.
    });

    // ── Security groups ───────────────────────────────────────────────────
    const albSg = new ec2.SecurityGroup(this, "AlbSg", {
      vpc,
      description: `One Fintech api-server ALB (${env}) - internal`,
      allowAllOutbound: false,
    });
    // Allow inbound HTTP from within the VPC (Grafana, other ECS tasks, VPN clients)
    albSg.addIngressRule(
      ec2.Peer.ipv4(vpc.vpcCidrBlock),
      ec2.Port.tcp(80),
      "Internal VPC HTTP to API server"
    );
    albSg.addEgressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(8080), "ALB to api-server containers");

    const taskSg = new ec2.SecurityGroup(this, "TaskSg", {
      vpc,
      description: `One Fintech api-server Fargate tasks (${env})`,
      allowAllOutbound: false,
    });
    taskSg.addIngressRule(albSg, ec2.Port.tcp(8080), "From ALB only");
    taskSg.addEgressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(443), "HTTPS to AWS APIs");
    taskSg.addEgressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(5432), "PostgreSQL to RDS Proxy");

    // ── Internal Application Load Balancer ────────────────────────────────
    const alb = new elbv2.ApplicationLoadBalancer(this, "Alb", {
      vpc,
      internetFacing: false,                // internal only
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      securityGroup: albSg,
      loadBalancerName: `onefintech-${env}-api`,
    });
    this.albDns = alb.loadBalancerDnsName;

    const targetGroup = new elbv2.ApplicationTargetGroup(this, "TargetGroup", {
      vpc,
      port: 8080,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targetType: elbv2.TargetType.IP,
      healthCheck: {
        path: "/healthz",
        interval: cdk.Duration.seconds(30),
        healthyHttpCodes: "200",
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 3,
      },
    });

    alb.addListener("HttpListener", {
      port: 80,
      protocol: elbv2.ApplicationProtocol.HTTP,
      defaultTargetGroups: [targetGroup],
    });

    // ── Fargate service ───────────────────────────────────────────────────
    this.service = new ecs.FargateService(this, "Service", {
      cluster,
      taskDefinition: taskDef,
      serviceName: `onefintech-${env}-api-server`,
      desiredCount: 1,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      securityGroups: [taskSg],
      assignPublicIp: false,
      enableECSManagedTags: true,
      // Rolling update: keep 100% minimum, 200% maximum — zero downtime deploys
      minHealthyPercent: 100,
      maxHealthyPercent: 200,
    });

    this.service.attachToApplicationTargetGroup(targetGroup);

    // Grant ECR pull to execution role
    this.repository.grantPull(executionRole);

    // ── Outputs ───────────────────────────────────────────────────────────
    new cdk.CfnOutput(this, "ApiServerAlbDns", {
      value: alb.loadBalancerDnsName,
      description: "One Fintech Card and Member API ALB DNS - append /docs/ for Swagger UI",
    });
    new cdk.CfnOutput(this, "ApiServerEcrUri", {
      value: this.repository.repositoryUri,
      description: "ECR repository for api-server image",
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", env);
  }
}
