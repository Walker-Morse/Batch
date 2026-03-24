import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as ecs from "aws-cdk-lib/aws-ecs";
import * as ecr from "aws-cdk-lib/aws-ecr";
import * as iam from "aws-cdk-lib/aws-iam";
import * as logs from "aws-cdk-lib/aws-logs";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import { Construct } from "constructs";

export interface EcsProps {
  env: string;
  vpc: ec2.Vpc;
  taskRole: iam.Role;
  executionRole: iam.Role;
  dbSecret: secretsmanager.ISecret;       // Aurora master secret (for proxy auth grant)
  ingestTaskSecret: secretsmanager.ISecret; // onefintech/dev/db/ingest-task — runtime credential
  dbProxyEndpoint: string;
  inboundBucketName: string;
  stagedBucketName: string;
  fisExchangeBucketName: string;
  egressBucketName: string;
  kmsKeyArn: string;
  fisCompanyId: string;
}

/**
 * EcsConstruct — ECS Fargate cluster, task definition, ECR repository (ADR-003).
 *
 * Single-container design: one image, one task def, one log group.
 * Task sizing: 1 vCPU / 2 GB RAM for DEV. Right-size before UAT via load test.
 * 6-year CloudWatch log retention for HIPAA §164.312(b).
 *
 * CORRELATION_ID, S3_BUCKET, S3_KEY, FILE_TYPE, TENANT_ID, CLIENT_ID
 * are injected by the EventBridge Scheduler or S3 event trigger at invocation time.
 */
export class EcsConstruct extends Construct {
  public readonly cluster: ecs.Cluster;
  public readonly taskDefinition: ecs.FargateTaskDefinition;
  public readonly repository: ecr.Repository;
  public readonly taskSecurityGroup: ec2.SecurityGroup;

  constructor(scope: Construct, id: string, props: EcsProps) {
    super(scope, id);

    this.repository = new ecr.Repository(this, "Repository", {
      repositoryName: `onefintech-${props.env}/ingest-task`,
      imageScanOnPush: true,
      removalPolicy: props.env === "dev" ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN,
      lifecycleRules: [{ maxImageCount: 10, rulePriority: 1 }],
    });

    const logGroup = new logs.LogGroup(this, "LogGroup", {
      logGroupName: `/onefintech/${props.env}/ingest-task`,
      retention: logs.RetentionDays.SIX_YEARS,
      removalPolicy: props.env === "dev" ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN,
    });

    this.cluster = new ecs.Cluster(this, "Cluster", {
      clusterName: `onefintech-${props.env}`,
      vpc: props.vpc,
      containerInsights: true,
    });

    this.taskDefinition = new ecs.FargateTaskDefinition(this, "TaskDef", {
      family: `onefintech-${props.env}-ingest-task`,
      cpu: 1024,       // 1 vCPU — validate under 200K-row load before UAT
      memoryLimitMiB: 2048,
      taskRole: props.taskRole,
      executionRole: props.executionRole,
    });

    this.taskDefinition.addContainer("ingest-task", {
      image: ecs.ContainerImage.fromEcrRepository(this.repository, "latest"),
      environment: {
        PIPELINE_ENV:        props.env.toUpperCase(),
        AWS_REGION:          cdk.Stack.of(this).region,
        DB_HOST:             props.dbProxyEndpoint,
        DB_NAME:             "onefintech",
        DB_USER:             "ingest_task",
        DB_SSL:              "require",
        STAGED_BUCKET:       props.stagedBucketName,
        FIS_EXCHANGE_BUCKET: props.fisExchangeBucketName,
        EGRESS_BUCKET:       props.egressBucketName,
        FIS_COMPANY_ID:      props.fisCompanyId,
      },
      // DB_PASSWORD injected at task startup by ECS from the ingestTaskSecret JSON key 'password'.
      // fromSecretsManager(secret, 'password') sets valueFrom to the full secret ARN + ':password::',
      // which ECS resolves by calling GetSecretValue on the execution role and extracting the key.
      // DB_SECRET_ARN is intentionally removed — the app reads DB_PASSWORD (main.go:592).
      secrets: {
        DB_PASSWORD: ecs.Secret.fromSecretsManager(props.ingestTaskSecret, 'password'),
      },
      logging: ecs.LogDrivers.awsLogs({ streamPrefix: "ingest-task", logGroup }),
      essential: true,
    });

    this.taskSecurityGroup = new ec2.SecurityGroup(this, "TaskSg", {
      vpc: props.vpc,
      description: "One Fintech ingest-task Fargate tasks",
      allowAllOutbound: false,
    });
    this.taskSecurityGroup.addEgressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(443),
      "HTTPS - AWS APIs via VPC endpoints");
    this.taskSecurityGroup.addEgressRule(ec2.Peer.anyIpv4(), ec2.Port.tcp(5432),
      "PostgreSQL to RDS Proxy");
    // Port 22 egress removed: Stage 5 now writes to S3 egress bucket (no SFTP delivery).
    // FIS Transfer Family SFTP is inbound-only (Stage 6 return file polling via S3).

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", props.env);
  }
}
