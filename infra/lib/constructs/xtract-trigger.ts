import * as cdk from "aws-cdk-lib";
import * as events from "aws-cdk-lib/aws-events";
import * as targets from "aws-cdk-lib/aws-events-targets";
import * as ecs from "aws-cdk-lib/aws-ecs";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as iam from "aws-cdk-lib/aws-iam";
import * as s3 from "aws-cdk-lib/aws-s3";
import { Construct } from "constructs";

export interface XtractTriggerProps {
  env: string;
  xtractBucket: s3.IBucket;
  cluster: ecs.ICluster;
  taskDefinition: ecs.FargateTaskDefinition;
  taskSecurityGroup: ec2.ISecurityGroup;
  subnetIds: string[];
  taskRole: iam.IRole;
  executionRole: iam.IRole;
}

/**
 * XtractTriggerConstruct — S3 ObjectCreated on xtract bucket → ECS xtract-loader task.
 *
 * Pattern mirrors TriggerConstruct (SRG inbound-raw trigger) exactly.
 * EventBridge injects S3_BUCKET and S3_KEY as container environment overrides.
 * TENANT_ID is inferred inside the task from the S3 key prefix.
 *
 * File suffix filter accepts all .txt files (all XTRACT feeds use .txt).
 * The xtract-loader binary dispatches to the correct feed parser based on filename prefix.
 *
 * Requires eventBridgeEnabled: true on xtract bucket (set in StorageConstruct).
 */
export class XtractTriggerConstruct extends Construct {
  public readonly triggerRole: iam.Role;

  constructor(scope: Construct, id: string, props: XtractTriggerProps) {
    super(scope, id);

    // IAM role allowing EventBridge to invoke ECS RunTask
    this.triggerRole = new iam.Role(this, "XtractTriggerRole", {
      roleName: `onefintech-${props.env}-xtract-trigger-role`,
      assumedBy: new iam.ServicePrincipal("events.amazonaws.com"),
      description: "EventBridge to ECS RunTask for XTRACT file arrival",
    });

    this.triggerRole.addToPolicy(new iam.PolicyStatement({
      sid: "AllowRunXtractTask",
      actions: ["ecs:RunTask"],
      resources: [props.taskDefinition.taskDefinitionArn],
    }));

    this.triggerRole.addToPolicy(new iam.PolicyStatement({
      sid: "AllowPassXtractRoles",
      actions: ["iam:PassRole"],
      resources: [
        props.taskRole.roleArn,
        props.executionRole.roleArn,
      ],
      conditions: {
        StringLike: { "iam:PassedToService": "ecs-tasks.amazonaws.com" },
      },
    }));

    // EventBridge rule: all .txt ObjectCreated events on the xtract bucket
    const rule = new events.Rule(this, "XtractS3TriggerRule", {
      ruleName: `onefintech-${props.env}-xtract-s3-trigger`,
      description: "S3 ObjectCreated on xtract bucket triggers xtract-loader ECS task",
      eventPattern: {
        source: ["aws.s3"],
        detailType: ["Object Created"],
        detail: {
          bucket: {
            name: [props.xtractBucket.bucketName],
          },
          object: {
            key: [{ suffix: ".txt" }],
          },
        },
      },
    });

    rule.addTarget(
      new targets.EcsTask({
        cluster: props.cluster,
        taskDefinition: props.taskDefinition,
        launchType: ecs.LaunchType.FARGATE,
        role: this.triggerRole,
        securityGroups: [props.taskSecurityGroup],
        subnetSelection: {
          subnets: props.subnetIds.map((id) =>
            ec2.Subnet.fromSubnetId(scope, `XtractTriggerSubnet-${id}`, id)
          ),
        },
        containerOverrides: [
          {
            containerName: "xtract-loader",
            environment: [
              {
                name: "S3_BUCKET",
                value: events.EventField.fromPath("$.detail.bucket.name"),
              },
              {
                name: "S3_KEY",
                value: events.EventField.fromPath("$.detail.object.key"),
              },
            ],
          },
        ],
      })
    );

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", props.env);
  }
}
