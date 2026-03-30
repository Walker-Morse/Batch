import * as cdk from "aws-cdk-lib";
import * as ecs from "aws-cdk-lib/aws-ecs";
import * as ecr from "aws-cdk-lib/aws-ecr";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as iam from "aws-cdk-lib/aws-iam";
import * as logs from "aws-cdk-lib/aws-logs";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import * as s3 from "aws-cdk-lib/aws-s3";
import * as kms from "aws-cdk-lib/aws-kms";
import { Construct } from "constructs";

export interface XtractEcsProps {
  env: string;
  cluster: ecs.ICluster;
  taskRole: iam.IRole;
  executionRole: iam.IRole;
  ingestTaskSecret: secretsmanager.ISecret;
  dbProxyEndpoint: string;
  xtractBucket: s3.IBucket;
  kmsKeyArn: string;
}

/**
 * XtractEcsConstruct — ECS Fargate task definition for the xtract-loader binary.
 *
 * Separate task from ingest-task:
 *   - Different image (xtract-loader binary, not ingest-task)
 *   - Different CloudWatch log group (/onefintech/{env}/xtract-loader)
 *   - Same cluster, same VPC subnets, same Aurora proxy
 *   - Shares ingest-task IAM role (already has XtractRead on xtract bucket)
 *
 * EventBridge trigger (XtractTriggerConstruct) fires this task on
 * S3 ObjectCreated events on the xtract bucket.
 *
 * Container environment:
 *   S3_BUCKET  — injected by EventBridge at runtime
 *   S3_KEY     — injected by EventBridge at runtime
 *   TENANT_ID  — inferred from S3_KEY prefix inside the task
 *   DB_*       — same Aurora proxy as ingest-task
 */
export class XtractEcsConstruct extends Construct {
  public readonly taskDefinition: ecs.FargateTaskDefinition;
  public readonly repository: ecr.Repository;
  public readonly taskSecurityGroup: ec2.SecurityGroup;

  constructor(scope: Construct, id: string, props: XtractEcsProps) {
    super(scope, id);

    const { env } = props;

    // ECR repository for xtract-loader image
    this.repository = new ecr.Repository(this, "XtractRepository", {
      repositoryName: `onefintech-${env}/xtract-loader`,
      removalPolicy: env === "dev" ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN,
      lifecycleRules: [{ maxImageCount: 10, description: "keep last 10 images" }],
    });

    // CloudWatch log group — separate from ingest-task for clean signal isolation
    const logGroup = new logs.LogGroup(this, "XtractLogGroup", {
      logGroupName: `/onefintech/${env}/xtract-loader`,
      retention: logs.RetentionDays.SIX_YEARS, // §164.312(b) HIPAA retention
      removalPolicy: env === "dev" ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN,
    });

    // Security group — same outbound rules as ingest-task (Aurora + S3 via VPC endpoints)
    this.taskSecurityGroup = new ec2.SecurityGroup(this, "XtractTaskSG", {
      securityGroupName: `onefintech-${env}-xtract-loader-sg`,
      vpc: (props.cluster as any).vpc,
      description: "xtract-loader ECS task security group",
      allowAllOutbound: true,
    });

    // Fargate task definition
    // Memory: 1GB is sufficient for pipe-delimited parsing (streaming, no full load into RAM)
    // CPU: 512 (0.5 vCPU) — I/O bound not CPU bound
    this.taskDefinition = new ecs.FargateTaskDefinition(this, "XtractTaskDef", {
      family: `onefintech-${env}-xtract-loader`,
      taskRole: props.taskRole,
      executionRole: props.executionRole,
      cpu: 512,
      memoryLimitMiB: 1024,
    });

    this.taskDefinition.addContainer("xtract-loader", {
      image: ecs.ContainerImage.fromEcrRepository(this.repository, "latest"),
      environment: {
        PIPELINE_ENV: env.toUpperCase(),
        AWS_REGION:   cdk.Stack.of(this).region,
        DB_HOST:      props.dbProxyEndpoint,
        DB_NAME:      "onefintech",
        DB_USER:      "ingest_task",
        DB_SSL:       "require",
        XTRACT_BUCKET: props.xtractBucket.bucketName,
      },
      secrets: {
        // Same DB credential pattern as ingest-task
        DB_PASSWORD: ecs.Secret.fromSecretsManager(props.ingestTaskSecret, "password"),
      },
      logging: ecs.LogDrivers.awsLogs({
        streamPrefix: "xtract-loader",
        logGroup,
      }),
      essential: true,
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", env);

    new cdk.CfnOutput(scope, "XtractEcrRepositoryUri", {
      value: this.repository.repositoryUri,
      description: "ECR URI for xtract-loader image",
    });
  }
}
