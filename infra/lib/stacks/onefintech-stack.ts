import * as cdk from "aws-cdk-lib";
import { Construct } from "constructs";
import { NetworkingConstruct } from "../constructs/networking";
import { StorageConstruct } from "../constructs/storage";
import { AuroraConstruct } from "../constructs/aurora";
import { IamConstruct } from "../constructs/iam";
import { EcsConstruct } from "../constructs/ecs";
import { SchedulerConstruct } from "../constructs/scheduler";
import { TriggerConstruct } from "../constructs/trigger";

export interface OneFintechStackProps extends cdk.StackProps {
  environment: "dev" | "tst" | "prd";
  auroraMinAcu: number;
  auroraMaxAcu: number;
  fisCompanyId: string;
  scheduleCron: string;
  natGateways: number;
  pgpPrivateKeySecretArn: string;
  pgpPassphraseSecretArn: string;
  pgpFisPublicKeySecretArn: string;
}

/**
 * OneFintechStack wires all constructs for the One Fintech / FIS Prepaid Sunrise pipeline.
 *
 * Topology (ADR-003, ADR-005, ADR-007, ADR-008):
 *   VPC → Aurora Serverless v2 (RDS Proxy) → ECS Fargate (ingest-task)
 *   S3 (inbound-raw, staged, fis-exchange) → EventBridge Scheduler → ECS RunTask
 *   Secrets Manager → KMS → IAM task role (least-privilege per §5.4.5)
 */
export class OneFintechStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: OneFintechStackProps) {
    super(scope, id, props);

    const { environment: env, auroraMinAcu, auroraMaxAcu, fisCompanyId, scheduleCron, natGateways,
            pgpPrivateKeySecretArn, pgpPassphraseSecretArn, pgpFisPublicKeySecretArn } = props;

    const networking = new NetworkingConstruct(this, "Networking", { env, natGateways });
    const storage = new StorageConstruct(this, "Storage", { env });
    const aurora = new AuroraConstruct(this, "Aurora", {
      env, vpc: networking.vpc, minAcu: auroraMinAcu, maxAcu: auroraMaxAcu,
    });
    const iam = new IamConstruct(this, "Iam", {
      env,
      inboundBucket: storage.inboundBucket,
      stagedBucket: storage.stagedBucket,
      fisExchangeBucket: storage.fisExchangeBucket,
      dbSecret: aurora.dbSecret,
      kmsKey: storage.kmsKey,
      pgpPrivateKeySecretArn,
      pgpPassphraseSecretArn,
      pgpFisPublicKeySecretArn,
    });
    const ecs = new EcsConstruct(this, "Ecs", {
      env,
      vpc: networking.vpc,
      taskRole: iam.taskRole,
      executionRole: iam.executionRole,
      dbSecret: aurora.dbSecret,
      dbProxyEndpoint: aurora.proxyEndpoint,
      inboundBucketName: storage.inboundBucket.bucketName,
      stagedBucketName: storage.stagedBucket.bucketName,
      fisExchangeBucketName: storage.fisExchangeBucket.bucketName,
      kmsKeyArn: storage.kmsKey.keyArn,
      fisCompanyId,
    });
    new SchedulerConstruct(this, "Scheduler", {
      env,
      scheduleCron,
      taskDefinitionArn: ecs.taskDefinition.taskDefinitionArn,
      clusterArn: ecs.cluster.clusterArn,
      subnetIds: networking.vpc.privateSubnets.map((s) => s.subnetId),
      securityGroupId: ecs.taskSecurityGroup.securityGroupId,
      schedulerRoleArn: iam.schedulerRole.roleArn,
    });

    // S3 file-arrival trigger: inbound-raw ObjectCreated → ECS RunTask (ADR-006b)
    new TriggerConstruct(this, "Trigger", {
      env,
      inboundBucket: storage.inboundBucket,
      cluster: ecs.cluster,
      taskDefinition: ecs.taskDefinition,
      taskSecurityGroup: ecs.taskSecurityGroup,
      subnetIds: networking.vpc.privateSubnets.map((s) => s.subnetId),
      taskRole: iam.taskRole,
      executionRole: iam.executionRole,
    });

    new cdk.CfnOutput(this, "InboundBucketName",    { value: storage.inboundBucket.bucketName });
    new cdk.CfnOutput(this, "StagedBucketName",     { value: storage.stagedBucket.bucketName });
    new cdk.CfnOutput(this, "FisExchangeBucketName",{ value: storage.fisExchangeBucket.bucketName });
    new cdk.CfnOutput(this, "ClusterArn",           { value: ecs.cluster.clusterArn });
    new cdk.CfnOutput(this, "TaskDefinitionArn",    { value: ecs.taskDefinition.taskDefinitionArn });
    new cdk.CfnOutput(this, "EcrRepositoryUri",     { value: ecs.repository.repositoryUri });
    new cdk.CfnOutput(this, "AuroraProxyEndpoint",  { value: aurora.proxyEndpoint });
    new cdk.CfnOutput(this, "DbSecretArn",          { value: aurora.dbSecret.secretArn });
  }
}
