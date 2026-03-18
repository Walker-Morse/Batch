import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import { Construct } from "constructs";

export interface NetworkingProps { env: string; natGateways: number; }

export class NetworkingConstruct extends Construct {
  public readonly vpc: ec2.Vpc;

  constructor(scope: Construct, id: string, props: NetworkingProps) {
    super(scope, id);
    this.vpc = new ec2.Vpc(this, "Vpc", {
      vpcName: `onefintech-${props.env}`,
      maxAzs: 2,
      natGateways: props.natGateways,
      subnetConfiguration: [
        { name: "Public",   subnetType: ec2.SubnetType.PUBLIC,                cidrMask: 24 },
        { name: "Private",  subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS,   cidrMask: 24 },
        { name: "Isolated", subnetType: ec2.SubnetType.PRIVATE_ISOLATED,      cidrMask: 28 },
      ],
    });
    // S3 gateway endpoint — free; avoids NAT for large batch file transfers
    this.vpc.addGatewayEndpoint("S3Endpoint", {
      service: ec2.GatewayVpcEndpointAwsService.S3,
      subnets: [{ subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS }],
    });
    // Interface endpoints — ECS Fargate needs these without internet access
    this.vpc.addInterfaceEndpoint("SecretsManagerEndpoint", {
      service: ec2.InterfaceVpcEndpointAwsService.SECRETS_MANAGER,
      subnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      privateDnsEnabled: true,
    });
    this.vpc.addInterfaceEndpoint("EcrEndpoint", {
      service: ec2.InterfaceVpcEndpointAwsService.ECR,
      subnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      privateDnsEnabled: true,
    });
    this.vpc.addInterfaceEndpoint("EcrDockerEndpoint", {
      service: ec2.InterfaceVpcEndpointAwsService.ECR_DOCKER,
      subnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      privateDnsEnabled: true,
    });
    this.vpc.addInterfaceEndpoint("CloudWatchLogsEndpoint", {
      service: ec2.InterfaceVpcEndpointAwsService.CLOUDWATCH_LOGS,
      subnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      privateDnsEnabled: true,
    });
    cdk.Tags.of(this.vpc).add("Project", "OneFintechFIS");
    cdk.Tags.of(this.vpc).add("Environment", props.env);
  }
}
