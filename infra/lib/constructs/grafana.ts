import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as ecs from "aws-cdk-lib/aws-ecs";
import * as iam from "aws-cdk-lib/aws-iam";
import * as elbv2 from "aws-cdk-lib/aws-elasticloadbalancingv2";
import * as logs from "aws-cdk-lib/aws-logs";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import * as efs from "aws-cdk-lib/aws-efs";
import { Construct } from "constructs";

export interface GrafanaProps {
  env: string;
  vpc: ec2.IVpc;
  cluster: ecs.ICluster;
}

/**
 * GrafanaConstruct — self-hosted Grafana on ECS Fargate behind an ALB.
 *
 * Architecture:
 *   Internet → ALB (public subnets, port 80) → Grafana ECS service (private subnets, port 3000)
 *   Grafana task role → CloudWatch read-only (logs + metrics, onefintech-dev-* only)
 *   Grafana state (dashboards, users, datasources) → EFS volume (persists across task restarts)
 *   Admin password → Secrets Manager (onefintech/{env}/grafana/admin-password)
 *
 * Access model for UAT team (5-10 people):
 *   Admin (Kyle): ALB URL + admin password from Secrets Manager
 *   Testers: ALB URL + Viewer role account (created by admin in Grafana UI)
 *   No AWS account required for testers — Grafana login only
 *
 * IAM scoping (zero pipeline access):
 *   Grafana task role has ONLY:
 *     - logs:StartQuery, logs:GetQueryResults, logs:DescribeLogGroups on /onefintech/dev/*
 *     - cloudwatch:GetMetricData, cloudwatch:ListMetrics on onefintech-dev-* dashboards
 *   No S3, no Aurora, no Secrets Manager, no ECS access whatsoever.
 *
 * Cost (DEV):
 *   Fargate: 0.25 vCPU / 512MB = ~$6/month
 *   ALB: ~$18/month
 *   EFS: negligible (<1GB)
 *   Total: ~$24/month — stop the service when not in use to save cost
 */
export class GrafanaConstruct extends Construct {
  public readonly serviceUrl: string;
  public readonly adminPasswordSecretArn: string;

  constructor(scope: Construct, id: string, props: GrafanaProps) {
    super(scope, id);

    const { env, vpc, cluster } = props;

    // ── Admin password in Secrets Manager ────────────────────────────────
    const adminPassword = new secretsmanager.Secret(this, "AdminPassword", {
      secretName: `onefintech/${env}/grafana/admin-password`,
      description: "Grafana admin password — share only with Kyle Walker",
      generateSecretString: {
        excludePunctuation: false,
        includeSpace: false,
        passwordLength: 24,
      },
    });
    this.adminPasswordSecretArn = adminPassword.secretArn;

    // ── EFS for persistent Grafana state ─────────────────────────────────
    const efsSg = new ec2.SecurityGroup(this, "EfsSg", {
      vpc,
      description: "Grafana EFS mount target",
      allowAllOutbound: false,
    });

    const fileSystem = new efs.FileSystem(this, "GrafanaEfs", {
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      securityGroup: efsSg,
      encrypted: true,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
      lifecyclePolicy: efs.LifecyclePolicy.AFTER_30_DAYS,
    });

    const accessPoint = fileSystem.addAccessPoint("GrafanaAccessPoint", {
      path: "/grafana",
      createAcl: { ownerGid: "472", ownerUid: "472", permissions: "755" },
      posixUser:  { gid: "472", uid: "472" },
    });

    // ── Read-only IAM role for Grafana → CloudWatch ───────────────────────
    const grafanaTaskRole = new iam.Role(this, "GrafanaTaskRole", {
      roleName: `onefintech-${env}-grafana-task-role`,
      assumedBy: new iam.ServicePrincipal("ecs-tasks.amazonaws.com"),
      description: "Grafana read-only access to OneFintechDev CloudWatch logs and metrics",
    });

    // CloudWatch Logs Insights — scoped to onefintech log groups only
    grafanaTaskRole.addToPolicy(new iam.PolicyStatement({
      sid: "CloudWatchLogsReadOnly",
      actions: [
        "logs:StartQuery",
        "logs:StopQuery",
        "logs:GetQueryResults",
        "logs:GetLogEvents",
        "logs:FilterLogEvents",
        "logs:DescribeLogGroups",
        "logs:DescribeLogStreams",
      ],
      resources: [
        `arn:aws:logs:${cdk.Stack.of(this).region}:${cdk.Stack.of(this).account}:log-group:/onefintech/${env}/*`,
        `arn:aws:logs:${cdk.Stack.of(this).region}:${cdk.Stack.of(this).account}:log-group:/onefintech/${env}/*:*`,
      ],
    }));

    // CloudWatch Metrics — read-only, all resources (metrics don't have resource-level scoping)
    grafanaTaskRole.addToPolicy(new iam.PolicyStatement({
      sid: "CloudWatchMetricsReadOnly",
      actions: [
        "cloudwatch:GetMetricData",
        "cloudwatch:GetMetricStatistics",
        "cloudwatch:ListMetrics",
        "cloudwatch:DescribeAlarms",
      ],
      resources: ["*"],
    }));

    // EFS mount access
    fileSystem.grantRootAccess(grafanaTaskRole);

    // Secrets Manager — read admin password only (Grafana needs it at startup)
    adminPassword.grantRead(grafanaTaskRole);

    // ── Execution role ────────────────────────────────────────────────────
    const grafanaExecutionRole = new iam.Role(this, "GrafanaExecutionRole", {
      roleName: `onefintech-${env}-grafana-execution-role`,
      assumedBy: new iam.ServicePrincipal("ecs-tasks.amazonaws.com"),
      managedPolicies: [
        iam.ManagedPolicy.fromAwsManagedPolicyName("service-role/AmazonECSTaskExecutionRolePolicy"),
      ],
    });
    adminPassword.grantRead(grafanaExecutionRole);

    // ── Log group ─────────────────────────────────────────────────────────
    const logGroup = new logs.LogGroup(this, "GrafanaLogGroup", {
      logGroupName: `/onefintech/${env}/grafana`,
      retention: logs.RetentionDays.ONE_MONTH,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // ── Task definition ───────────────────────────────────────────────────
    const taskDef = new ecs.FargateTaskDefinition(this, "GrafanaTaskDef", {
      family: `onefintech-${env}-grafana`,
      cpu: 256,
      memoryLimitMiB: 512,
      taskRole: grafanaTaskRole,
      executionRole: grafanaExecutionRole,
      volumes: [{
        name: "grafana-storage",
        efsVolumeConfiguration: {
          fileSystemId: fileSystem.fileSystemId,
          transitEncryption: "ENABLED",
          authorizationConfig: {
            accessPointId: accessPoint.accessPointId,
            iam: "ENABLED",
          },
        },
      }],
    });

    const container = taskDef.addContainer("grafana", {
      image: ecs.ContainerImage.fromRegistry("grafana/grafana:10.4.3"),
      portMappings: [{ containerPort: 3000 }],
      environment: {
        GF_SERVER_ROOT_URL:          `http://onefintech-${env}-grafana.internal`,
        GF_AUTH_ANONYMOUS_ENABLED:   "false",
        GF_SECURITY_ADMIN_USER:      "admin",
        GF_INSTALL_PLUGINS:          "grafana-cloudwatch-datasource",
        GF_PATHS_DATA:               "/var/lib/grafana",
        GF_LOG_MODE:                 "console",
        GF_LOG_LEVEL:                "info",
        AWS_DEFAULT_REGION:          cdk.Stack.of(this).region,
      },
      secrets: {
        GF_SECURITY_ADMIN_PASSWORD: ecs.Secret.fromSecretsManager(adminPassword),
      },
      logging: ecs.LogDrivers.awsLogs({
        streamPrefix: "grafana",
        logGroup,
      }),
      essential: true,
    });

    container.addMountPoints({
      sourceVolume:   "grafana-storage",
      containerPath:  "/var/lib/grafana",
      readOnly:       false,
    });

    // ── Security groups ───────────────────────────────────────────────────
    const albSg = new ec2.SecurityGroup(this, "AlbSg", {
      vpc,
      description: "Grafana ALB — allow HTTP inbound",
      allowAllOutbound: true,
    });
    albSg.addIngressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(80), "HTTP from internet");

    const grafanaSg = new ec2.SecurityGroup(this, "GrafanaSg", {
      vpc,
      description: "Grafana ECS task",
      allowAllOutbound: false,
    });
    grafanaSg.addIngressRule(albSg, ec2.Port.tcp(3000), "ALB to Grafana");
    grafanaSg.addEgressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(443), "HTTPS to AWS APIs");

    // Allow Grafana task to reach EFS
    efsSg.addIngressRule(grafanaSg, ec2.Port.tcp(2049), "EFS from Grafana task");
    grafanaSg.addEgressRule(efsSg, ec2.Port.tcp(2049), "EFS mount");

    // ── ALB ───────────────────────────────────────────────────────────────
    const alb = new elbv2.ApplicationLoadBalancer(this, "Alb", {
      loadBalancerName: `onefintech-${env}-grafana`,
      vpc,
      internetFacing: true,
      vpcSubnets: { subnetType: ec2.SubnetType.PUBLIC },
      securityGroup: albSg,
    });

    const listener = alb.addListener("HttpListener", {
      port: 80,
      open: true,
    });

    // ── ECS Service ───────────────────────────────────────────────────────
    const service = new ecs.FargateService(this, "GrafanaService", {
      serviceName: `onefintech-${env}-grafana`,
      cluster,
      taskDefinition: taskDef,
      desiredCount: 1,
      assignPublicIp: false,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      securityGroups: [grafanaSg],
      enableExecuteCommand: false,
    });

    listener.addTargets("GrafanaTarget", {
      port: 3000,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targets: [service],
      healthCheck: {
        path: "/api/health",
        interval: cdk.Duration.seconds(30),
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 3,
      },
      deregistrationDelay: cdk.Duration.seconds(30),
    });

    this.serviceUrl = `http://${alb.loadBalancerDnsName}`;

    // ── Outputs ───────────────────────────────────────────────────────────
    new cdk.CfnOutput(scope, "GrafanaUrl", {
      value: this.serviceUrl,
      description: "Grafana URL — share with UAT team. Login: admin / (see Secrets Manager)",
    });
    new cdk.CfnOutput(scope, "GrafanaAdminPasswordArn", {
      value: this.adminPasswordSecretArn,
      description: "Grafana admin password ARN in Secrets Manager",
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", env);
  }
}
