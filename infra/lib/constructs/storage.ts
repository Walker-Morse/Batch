import * as cdk from "aws-cdk-lib";
import * as s3 from "aws-cdk-lib/aws-s3";
import * as kms from "aws-cdk-lib/aws-kms";
import { Construct } from "constructs";

export interface StorageProps { env: string; }

export class StorageConstruct extends Construct {
  public readonly kmsKey: kms.Key;
  public readonly inboundBucket: s3.Bucket;
  public readonly stagedBucket: s3.Bucket;
  public readonly fisExchangeBucket: s3.Bucket;
  private readonly logsBucket: s3.Bucket;

  constructor(scope: Construct, id: string, props: StorageProps) {
    super(scope, id);
    const isDev = props.env === "dev";
    const removalPolicy = isDev ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN;
    const autoDeleteObjects = isDev;

    this.kmsKey = new kms.Key(this, "PipelineKey", {
      alias: `onefintech-${props.env}-pipeline`,
      description: "One Fintech pipeline SSE-KMS key",
      enableKeyRotation: true,
      removalPolicy,
    });

    this.logsBucket = new s3.Bucket(this, "LogsBucket", {
      bucketName: `onefintech-${props.env}-access-logs-placeholder`,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      removalPolicy,
      autoDeleteObjects,
      lifecycleRules: [{ expiration: cdk.Duration.days(90), id: "expire-logs-90d" }],
    });

    // inbound-raw — write-once; ingest-task has GetObject only (no DeleteObject per §5.4.5)
    this.inboundBucket = new s3.Bucket(this, "InboundRawBucket", {
      bucketName: `onefintech-${props.env}-inbound-raw-placeholder`,
      encryptionKey: this.kmsKey,
      encryption: s3.BucketEncryption.KMS,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      versioned: true,
      serverAccessLogsBucket: this.logsBucket,
      serverAccessLogsPrefix: "inbound-raw/",
      removalPolicy,
      autoDeleteObjects,
      eventBridgeEnabled: true, // S3 ObjectCreated → EventBridge → ECS RunTask (ADR-006b)
    });

    // staged — 24h lifecycle on staged/ prefix is PHI safety backstop (§5.4.3)
    this.stagedBucket = new s3.Bucket(this, "StagedBucket", {
      bucketName: `onefintech-${props.env}-staged-placeholder`,
      encryptionKey: this.kmsKey,
      encryption: s3.BucketEncryption.KMS,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      versioned: true,
      serverAccessLogsBucket: this.logsBucket,
      serverAccessLogsPrefix: "staged/",
      removalPolicy,
      autoDeleteObjects,
      lifecycleRules: [{
        id: "staged-plaintext-24h-expiry",
        expiration: cdk.Duration.days(1),
        prefix: "staged/",
      }],
    });

    // fis-exchange — outbound PGP-encrypted files + inbound return files
    this.fisExchangeBucket = new s3.Bucket(this, "FisExchangeBucket", {
      bucketName: `onefintech-${props.env}-fis-exchange-placeholder`,
      encryptionKey: this.kmsKey,
      encryption: s3.BucketEncryption.KMS,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      versioned: true,
      serverAccessLogsBucket: this.logsBucket,
      serverAccessLogsPrefix: "fis-exchange/",
      removalPolicy,
      autoDeleteObjects,
      lifecycleRules: [{
        // TODO: transition to Glacier after 90 days before PRD (Open Item #36)
        id: "fis-exchange-7yr-retention",
        expiration: cdk.Duration.days(2555),
      }],
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", props.env);
  }
}
