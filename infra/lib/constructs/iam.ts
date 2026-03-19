import * as cdk from "aws-cdk-lib";
import * as iam from "aws-cdk-lib/aws-iam";
import * as s3 from "aws-cdk-lib/aws-s3";
import * as kms from "aws-cdk-lib/aws-kms";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import { Construct } from "constructs";

export interface IamProps {
  env: string;
  inboundBucket: s3.IBucket;
  stagedBucket: s3.IBucket;
  fisExchangeBucket: s3.IBucket;
  dbSecret: secretsmanager.ISecret;
  kmsKey: kms.IKey;
  /**
   * ARNs of Secrets Manager secrets for PGP keys.
   * - pgpPrivateKeySecretArn: Morse private key for Stage 2 inbound decrypt
   * - pgpPassphraseSecretArn: passphrase secret (optional, pass "" if key is unencrypted)
   * - pgpFisPublicKeySecretArn: FIS public key for Stage 4 outbound encrypt
   * All three must be provisioned before TST/PRD deploy (Open Item #41).
   */
  pgpPrivateKeySecretArn: string;
  pgpPassphraseSecretArn: string;
  pgpFisPublicKeySecretArn: string;
}

/**
 * IamConstruct — least-privilege IAM roles per ADR-005 §5.4.5.
 *
 * Task role scoping:
 *   inbound-raw:    GetObject/HeadObject only (no DeleteObject ever)
 *   staged/:        full CRUD (Stage 4 must delete plaintext after encrypt)
 *   fis-exchange/:  PutObject + GetObject + ListBucket (Stage 4 write + Stage 6 poll)
 *   Secrets Manager: GetSecretValue on DB secret only (PGP key ARNs added later)
 *   KMS:            GenerateDataKey + Decrypt + DescribeKey
 *   CloudWatch Logs: log delivery from structured stdout
 */
export class IamConstruct extends Construct {
  public readonly taskRole: iam.Role;
  public readonly executionRole: iam.Role;
  public readonly schedulerRole: iam.Role;

  constructor(scope: Construct, id: string, props: IamProps) {
    super(scope, id);

    this.taskRole = new iam.Role(this, "TaskRole", {
      roleName: `onefintech-${props.env}-ingest-task-role`,
      assumedBy: new iam.ServicePrincipal("ecs-tasks.amazonaws.com"),
      description: "One Fintech ingest-task - scoped to pipeline resources only",
    });

    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "InboundRawRead",
      actions: ["s3:GetObject", "s3:HeadObject"],
      resources: [props.inboundBucket.arnForObjects("*")],
    }));
    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "StagedReadWrite",
      actions: ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:HeadObject"],
      resources: [props.stagedBucket.arnForObjects("*")],
    }));
    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "FisExchangeReadWrite",
      actions: ["s3:GetObject", "s3:PutObject", "s3:HeadObject", "s3:ListBucket"],
      resources: [
        props.fisExchangeBucket.bucketArn,
        props.fisExchangeBucket.arnForObjects("*"),
      ],
    }));
    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "S3BucketList",
      actions: ["s3:ListBucket"],
      resources: [props.inboundBucket.bucketArn, props.stagedBucket.bucketArn],
    }));
    // Build PGP secret ARN list — filter empty strings (passphrase is optional)
    const pgpSecretArns = [
      props.pgpPrivateKeySecretArn,
      props.pgpPassphraseSecretArn,
      props.pgpFisPublicKeySecretArn,
    ].filter((arn) => arn !== "");

    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "SecretsManagerRead",
      actions: ["secretsmanager:GetSecretValue"],
      resources: [props.dbSecret.secretArn, ...pgpSecretArns],
    }));
    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "KmsUsage",
      actions: ["kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"],
      resources: [props.kmsKey.keyArn],
    }));
    this.taskRole.addToPolicy(new iam.PolicyStatement({
      sid: "CloudWatchLogs",
      actions: ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"],
      resources: [`arn:aws:logs:*:*:log-group:/onefintech/${props.env}/*`],
    }));

    this.executionRole = new iam.Role(this, "ExecutionRole", {
      roleName: `onefintech-${props.env}-ecs-execution-role`,
      assumedBy: new iam.ServicePrincipal("ecs-tasks.amazonaws.com"),
      managedPolicies: [
        iam.ManagedPolicy.fromAwsManagedPolicyName("service-role/AmazonECSTaskExecutionRolePolicy"),
      ],
    });
    this.executionRole.addToPolicy(new iam.PolicyStatement({
      sid: "KmsEcrDecrypt",
      actions: ["kms:Decrypt", "kms:DescribeKey"],
      resources: [props.kmsKey.keyArn],
    }));
    this.executionRole.addToPolicy(new iam.PolicyStatement({
      sid: "ExecutionRoleSecrets",
      actions: ["secretsmanager:GetSecretValue"],
      resources: [props.dbSecret.secretArn],
    }));

    this.schedulerRole = new iam.Role(this, "SchedulerRole", {
      roleName: `onefintech-${props.env}-scheduler-role`,
      assumedBy: new iam.ServicePrincipal("scheduler.amazonaws.com"),
      description: "EventBridge Scheduler -> ECS RunTask for nightly batch delivery (ADR-007)",
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", props.env);
  }
}
