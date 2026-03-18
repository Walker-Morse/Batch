import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as rds from "aws-cdk-lib/aws-rds";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import { Construct } from "constructs";

export interface AuroraProps {
  env: string;
  vpc: ec2.Vpc;
  minAcu: number;
  maxAcu: number;
}

/**
 * AuroraConstruct — Aurora PostgreSQL Serverless v2 + RDS Proxy (ADR-008).
 *
 * ACU min/max are EXPLICITLY set — never rely on defaults (ADR-005 constraint).
 * DEV: min 0.5 (scale-to-zero allowed); TST/PRD: min must be above zero.
 * Load test required before UAT for 200K-row sequential ingest workloads.
 */
export class AuroraConstruct extends Construct {
  public readonly cluster: rds.DatabaseCluster;
  public readonly proxy: rds.DatabaseProxy;
  public readonly dbSecret: secretsmanager.ISecret;
  public readonly proxyEndpoint: string;

  constructor(scope: Construct, id: string, props: AuroraProps) {
    super(scope, id);
    const isDev = props.env === "dev";
    const removalPolicy = isDev ? cdk.RemovalPolicy.DESTROY : cdk.RemovalPolicy.RETAIN;

    const dbSg = new ec2.SecurityGroup(this, "DbSg", {
      vpc: props.vpc,
      description: "Aurora cluster — allow RDS Proxy only",
      allowAllOutbound: false,
    });
    const proxySg = new ec2.SecurityGroup(this, "ProxySg", {
      vpc: props.vpc,
      description: "RDS Proxy — allow ECS task SG",
      allowAllOutbound: false,
    });
    dbSg.addIngressRule(proxySg, ec2.Port.tcp(5432), "RDS Proxy to Aurora");

    this.cluster = new rds.DatabaseCluster(this, "Cluster", {
      engine: rds.DatabaseClusterEngine.auroraPostgres({
        version: rds.AuroraPostgresEngineVersion.VER_15_4,
      }),
      writer: rds.ClusterInstance.serverlessV2("writer", { scaleWithWriter: true }),
      serverlessV2MinCapacity: props.minAcu,
      serverlessV2MaxCapacity: props.maxAcu,
      vpc: props.vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_ISOLATED },
      securityGroups: [dbSg],
      defaultDatabaseName: "onefintech",
      storageEncrypted: true,
      deletionProtection: !isDev,
      removalPolicy,
      backup: { retention: cdk.Duration.days(isDev ? 7 : 35) },
      cloudwatchLogsExports: ["postgresql"],
    });

    this.dbSecret = this.cluster.secret!;

    this.proxy = new rds.DatabaseProxy(this, "Proxy", {
      proxyTarget: rds.ProxyTarget.fromCluster(this.cluster),
      secrets: [this.dbSecret],
      vpc: props.vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      securityGroups: [proxySg],
      requireTLS: true,
      idleClientTimeout: cdk.Duration.minutes(30),
      maxConnectionsPercent: 90,
      dbProxyName: `onefintech-${props.env}-proxy`,
    });

    this.proxyEndpoint = this.proxy.endpoint;
    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", props.env);
  }
}
