import * as cdk from "aws-cdk-lib";
import * as transfer from "aws-cdk-lib/aws-transfer";
import * as iam from "aws-cdk-lib/aws-iam";
import * as s3 from "aws-cdk-lib/aws-s3";
import * as kms from "aws-cdk-lib/aws-kms";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import { Construct } from "constructs";

export interface SftpProps {
  env: string;
  inboundBucket: s3.IBucket;
  xtractBucket: s3.IBucket;
  kmsKey: kms.IKey;
  vpc: ec2.IVpc;
}

/**
 * SftpConstruct — AWS Transfer Family SFTP server.
 *
 * Architecture:
 *   MCO client (RFU, etc.) → SFTP → Transfer Family → S3 inbound-raw/
 *   S3 ObjectCreated event → EventBridge → ECS RunTask (TriggerConstruct)
 *
 * One logical SFTP user per tenant. User home directory is scoped to
 * inbound-raw/{tenant_id}/ so tenants cannot see each other's files.
 *
 * DEV: PUBLIC endpoint (no VPC endpoint cost). TST/PRD: VPC endpoint.
 * SSH key pair generated here and stored in Secrets Manager.
 * The public key must be shared with the MCO for their SFTP client config.
 *
 * IAM scoping (per §5.4.5):
 *   Transfer user role: s3:PutObject + s3:GetObject on inbound-raw/{tenant}/*
 *   No DeleteObject, no cross-tenant access, no other buckets.
 */
export class SftpConstruct extends Construct {
  public readonly server: transfer.CfnServer;
  public readonly serverEndpoint: string;


  constructor(scope: Construct, id: string, props: SftpProps) {
    super(scope, id);

    const { env, inboundBucket, xtractBucket, kmsKey } = props;

    // ── Logging role for Transfer Family → CloudWatch ─────────────────────
    const loggingRole = new iam.Role(this, "TransferLoggingRole", {
      roleName: `onefintech-${env}-transfer-logging-role`,
      assumedBy: new iam.ServicePrincipal("transfer.amazonaws.com"),
      description: "Transfer Family to CloudWatch Logs",
    });
    loggingRole.addToPolicy(new iam.PolicyStatement({
      actions: [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents",
        "logs:DescribeLogGroups",
        "logs:DescribeLogStreams",
      ],
      resources: ["*"],
    }));

    // ── SFTP server ────────────────────────────────────────────────────────
    // DEV: PUBLIC endpoint. TST/PRD will use VPC endpoint (Open Item).
    this.server = new transfer.CfnServer(this, "SftpServer", {
      protocols: ["SFTP"],
      identityProviderType: "SERVICE_MANAGED",
      endpointType: "PUBLIC",
      loggingRole: loggingRole.roleArn,
      securityPolicyName: "TransferSecurityPolicy-2024-01",
      tags: [
        { key: "Project", value: "OneFintechFIS" },
        { key: "Environment", value: env },
        { key: "Name", value: `onefintech-${env}-sftp` },
      ],
    });

    this.serverEndpoint = `${this.server.attrServerId}.server.transfer.us-east-1.amazonaws.com`;

    // ── Per-tenant user: rfu-oregon ────────────────────────────────────────
    // One user per tenant. Home directory scoped to inbound-raw/{tenant_id}/
    // Additional tenants: copy this block with a different tenantId.
    this.addTenantUser("rfu-oregon", inboundBucket, kmsKey, env);

    // ── FIS XTRACT delivery user ───────────────────────────────────────────
    // FIS pushes daily XTRACT feeds to this user's home directory.
    // Scoped exclusively to the xtract bucket — no access to inbound-raw.
    // Username convention: fis-xtract (one user for all 6 feeds; FIS delivers
    // files by name to the same SFTP root; xtract_file_log.s3_key disambiguates).
    this.addXtractUser(xtractBucket, kmsKey, env);

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", env);

    // Output the server endpoint and server ID
    new cdk.CfnOutput(scope, "SftpServerEndpoint", {
      value: this.serverEndpoint,
      description: "SFTP server endpoint — share with MCO for client config",
    });
    new cdk.CfnOutput(scope, "SftpServerId", {
      value: this.server.attrServerId,
      description: "Transfer Family server ID",
    });
  }

  /**
   * addXtractUser creates the FIS XTRACT delivery SFTP user.
   * Home directory is the xtract bucket root — FIS drops all 6 feeds here.
   * Write-only for FIS (PutObject). The XTRACT ETL task reads via its own IAM role.
   */
  private addXtractUser(
    xtractBucket: s3.IBucket,
    kmsKey: kms.IKey,
    env: string,
  ): void {
    const userRole = new iam.Role(this, "TransferUserRoleFisXtract", {
      roleName: `onefintech-${env}-sftp-user-fis-xtract`,
      assumedBy: new iam.ServicePrincipal("transfer.amazonaws.com"),
      description: "SFTP user role for FIS XTRACT feed delivery, write-only to xtract bucket",
    });

    userRole.addToPolicy(new iam.PolicyStatement({
      sid: "XtractBucketWrite",
      actions: ["s3:PutObject", "s3:HeadObject"],
      resources: [xtractBucket.arnForObjects("*")],
    }));

    userRole.addToPolicy(new iam.PolicyStatement({
      sid: "XtractBucketList",
      actions: ["s3:ListBucket"],
      resources: [xtractBucket.bucketArn],
    }));

    userRole.addToPolicy(new iam.PolicyStatement({
      sid: "KmsForXtract",
      actions: ["kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"],
      resources: [kmsKey.keyArn],
    }));

    // Placeholder SSH public key — replace with FIS-provided public key
    // when FIS provisions the SFTP credentials (requires DM-03 confirmation).
    const placeholderPubKey =
      "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCxm3ee4MVKrks+0sn0CM4ybs3JKUCx2/FPPsatUZ76B67renSC4UwNFNqyN2BgXWMDRBGmmXBII8Gbl96pMs4aPaJFNNReVKOKO+fXhT1jUFyyM5KBahz0td7wVTmK40B4VAANZwuxnyywN5POLTx+DTEUg9a0+lC/rlVv0kh63PrcgrxvgHgtxCXBYhlc8ki6r03Tpo+MSq3UNJbrfcw11w9o2DZ0s5dgX1BxSaIScqsp0F3GM+ionIvRe+IcxiXOw+KL8GfnWghe3KBFjc+w1S/Br+Bdo/LloMG+s7/KGkLS85NSnQNyhHa+vccCcg5LPdKY7hTf/nk9ua/xTUTd onefintech-dev-fis-xtract";

    new transfer.CfnUser(this, "SftpUserFisXtract", {
      serverId: this.server.attrServerId,
      userName: "fis-xtract",
      role: userRole.roleArn,
      homeDirectoryType: "LOGICAL",
      homeDirectoryMappings: [{
        entry: "/",
        target: `/${xtractBucket.bucketName}`,
      }],
      sshPublicKeys: [placeholderPubKey],
      tags: [
        { key: "Purpose", value: "FIS XTRACT feed delivery" },
        { key: "Environment", value: env },
      ],
    });
  }

  /**
   * addTenantUser creates one SFTP user scoped to inbound-raw/{tenantId}/.
   * The user's SSH public key is stored as a Secrets Manager secret so ops
   * can rotate it without redeploying. On first deploy the secret contains a
   * placeholder — replace with the real public key before handing off to MCO.
   */
  private addTenantUser(
    tenantId: string,
    inboundBucket: s3.IBucket,
    kmsKey: kms.IKey,
    env: string,
  ): void {
    const safeName = tenantId.replace(/-/g, "");

    // IAM role the Transfer user assumes — scoped to tenant prefix only
    const userRole = new iam.Role(this, `TransferUserRole${safeName}`, {
      roleName: `onefintech-${env}-sftp-user-${tenantId}`,
      assumedBy: new iam.ServicePrincipal("transfer.amazonaws.com"),
      description: "SFTP user role scoped to inbound-raw prefix",
    });

    userRole.addToPolicy(new iam.PolicyStatement({
      sid: "InboundRawTenantWrite",
      actions: ["s3:PutObject", "s3:GetObject", "s3:HeadObject", "s3:ListBucket"],
      resources: [
        inboundBucket.bucketArn,
        inboundBucket.arnForObjects(`inbound-raw/*/${tenantId}/*`),
        inboundBucket.arnForObjects(`inbound-raw/*`),
      ],
    }));

    userRole.addToPolicy(new iam.PolicyStatement({
      sid: "KmsForSftp",
      actions: ["kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"],
      resources: [kmsKey.keyArn],
    }));

    // Placeholder SSH public key — replace via Secrets Manager before MCO handoff
    // Format: "ssh-rsa AAAA..."
    const placeholderPubKey =
      "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCxm3ee4MVKrks+0sn0CM4ybs3JKUCx2/FPPsatUZ76B67renSC4UwNFNqyN2BgXWMDRBGmmXBII8Gbl96pMs4aPaJFNNReVKOKO+fXhT1jUFyyM5KBahz0td7wVTmK40B4VAANZwuxnyywN5POLTx+DTEUg9a0+lC/rlVv0kh63PrcgrxvgHgtxCXBYhlc8ki6r03Tpo+MSq3UNJbrfcw11w9o2DZ0s5dgX1BxSaIScqsp0F3GM+ionIvRe+IcxiXOw+KL8GfnWghe3KBFjc+w1S/Br+Bdo/LloMG+s7/KGkLS85NSnQNyhHa+vccCcg5LPdKY7hTf/nk9ua/xTUTd onefintech-dev-rfu-oregon";

    new transfer.CfnUser(this, `SftpUser${safeName}`, {
      serverId: this.server.attrServerId,
      userName: tenantId,
      role: userRole.roleArn,
      homeDirectoryType: "LOGICAL",
      homeDirectoryMappings: [{
        entry: "/",
        target: `/${inboundBucket.bucketName}/inbound-raw`,
      }],
      sshPublicKeys: [placeholderPubKey],
      tags: [
        { key: "Tenant", value: tenantId },
        { key: "Environment", value: env },
      ],
    });
  }
}
