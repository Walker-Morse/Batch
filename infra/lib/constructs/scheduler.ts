import * as cdk from "aws-cdk-lib";
import * as scheduler from "aws-cdk-lib/aws-scheduler";
import * as iam from "aws-cdk-lib/aws-iam";
import { Construct } from "constructs";

export interface SchedulerProps {
  env: string;
  scheduleCron: string;
  taskDefinitionArn: string;
  clusterArn: string;
  subnetIds: string[];
  securityGroupId: string;
  schedulerRoleArn: string;
}

/**
 * SchedulerConstruct — EventBridge Scheduler nightly delivery gate (ADR-007).
 *
 * Fires batch.assemble.nightly on configured cadence → ECS RunTask → ingest-task.
 * Assembler is idempotent if no new records exist since last run.
 * Adding a new client = new CDK deployment with a new SchedulerConstruct (or parameterised rule).
 *
 * TODO: confirm 06:00 Pacific cutoff with Kendra Williams (Open Item #29).
 * TODO: per-client schedule rules when additional health plan clients onboard.
 */
export class SchedulerConstruct extends Construct {
  constructor(scope: Construct, id: string, props: SchedulerProps) {
    super(scope, id);

    const schedulerRole = iam.Role.fromRoleArn(this, "SchedulerRole", props.schedulerRoleArn);
    schedulerRole.addToPrincipalPolicy(new iam.PolicyStatement({
      sid: "AllowEcsRunTask",
      actions: ["ecs:RunTask"],
      resources: [props.taskDefinitionArn],
    }));
    schedulerRole.addToPrincipalPolicy(new iam.PolicyStatement({
      sid: "AllowPassRole",
      actions: ["iam:PassRole"],
      resources: ["*"],
      conditions: { StringLike: { "iam:PassedToService": "ecs-tasks.amazonaws.com" } },
    }));

    new scheduler.CfnSchedule(this, "NightlySchedule", {
      name: `onefintech-${props.env}-nightly-delivery`,
      description: "Nightly FIS batch delivery gate - batch.assemble.nightly (ADR-007)",
      scheduleExpression: props.scheduleCron,
      scheduleExpressionTimezone: "UTC",
      flexibleTimeWindow: { mode: "FLEXIBLE", maximumWindowInMinutes: 15 },
      target: {
        arn: props.clusterArn,
        roleArn: props.schedulerRoleArn,
        ecsParameters: {
          taskDefinitionArn: props.taskDefinitionArn,
          launchType: "FARGATE",
          networkConfiguration: {
            awsvpcConfiguration: {
              subnets: props.subnetIds,
              securityGroups: [props.securityGroupId],
              assignPublicIp: "DISABLED",
            },
          },
        },
        retryPolicy: { maximumRetryAttempts: 2, maximumEventAgeInSeconds: 3600 },
      },
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", props.env);
  }
}
