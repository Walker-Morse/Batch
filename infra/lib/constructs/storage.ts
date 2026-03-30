import * as cdk from "aws-cdk-lib";
import * as s3 from "aws-cdk-lib/aws-s3";
import * as kms from "aws-cdk-lib/aws-kms";
import { Construct } from "constructs";

export interface StorageProps { env: string; }

export class StorageConstruct extends Construct {
  public readonly kmsKey: kms.Key;
  public readonly inboundBucket: s3.Bucket;
  public readonly xtractBucket: s3.Bucket;
  public readonly stagedBucket: s3.Bucket;
  public readonly fisExchangeBucket: s3.Bucket;
  public readonly egressBucket: s3.Bucket;
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

    // xtract — FIS Data XTRACT feeds delivered daily via SFTP (§4.3.13).
    // Separate from inbound-raw: different IAM policy, different trigger,
    // different retention. XTRACT files contain PHI — same KMS key, SSE-KMS.
    // 7-year retention: §6.4a billing retention requirement.
    // EventBridge enabled: S3 ObjectCreated → XTRACT ETL loader task (to be wired).
    this.xtractBucket = new s3.Bucket(this, "XtractBucket", {
      bucketName: `onefintech-${props.env}-xtract`,
      encryptionKey: this.kmsKey,
      encryption: s3.BucketEncryption.KMS,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      versioned: true,
      serverAccessLogsBucket: this.logsBucket,
      serverAccessLogsPrefix: "xtract/",
      removalPolicy,
      autoDeleteObjects,
      eventBridgeEnabled: true,
      lifecycleRules: [{
        id: "xtract-7yr-retention",
        expiration: cdk.Duration.days(2555),
      }],
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

    // scp-exchange — outbound PGP-encrypted files + inbound return files
    this.fisExchangeBucket = new s3.Bucket(this, "FisExchangeBucket", {
      bucketName: `onefintech-${props.env}-scp-exchange-placeholder`,
      encryptionKey: this.kmsKey,
      encryption: s3.BucketEncryption.KMS,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      versioned: true,
      serverAccessLogsBucket: this.logsBucket,
      serverAccessLogsPrefix: "scp-exchange/",
      removalPolicy,
      autoDeleteObjects,
      lifecycleRules: [{
        // TODO: transition to Glacier after 90 days before PRD (Open Item #36)
        id: "scp-exchange-7yr-retention",
        expiration: cdk.Duration.days(2555),
      }],
    });

    // egress — FIS-readable outbound files (replaces SSH/SCP Transfer Family delivery)
    // ingest-task has PutObject only. FIS IAM principal has GetObject + ListBucket only.
    // 7-day lifecycle: FIS SLA is same-day pickup; 7 days is generous safety margin.
    // Separate from fis-exchange to give FIS a clean, single-purpose bucket boundary
    // (§5.4.5 — no prefix-condition complexity, clean CloudTrail signal).
    this.egressBucket = new s3.Bucket(this, "EgressBucket", {
      bucketName: `onefintech-${props.env}-egress-placeholder`,
      encryptionKey: this.kmsKey,
      encryption: s3.BucketEncryption.KMS,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true,
      versioned: true,
      serverAccessLogsBucket: this.logsBucket,
      serverAccessLogsPrefix: "egress/",
      removalPolicy,
      autoDeleteObjects,
      lifecycleRules: [{
        id: "egress-7d-expiry",
        expiration: cdk.Duration.days(7),
      }],
    });

    cdk.Tags.of(this).add("Project", "OneFintechSCP");    cdk.Tags.of(this).add("Environment", props.env);
  }
}
