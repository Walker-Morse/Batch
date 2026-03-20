import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import * as iam from "aws-cdk-lib/aws-iam";
import * as lambda from "aws-cdk-lib/aws-lambda";
import * as apigwv2 from "aws-cdk-lib/aws-apigatewayv2";
import * as apigwv2integrations from "aws-cdk-lib/aws-apigatewayv2-integrations";
import * as logs from "aws-cdk-lib/aws-logs";
import * as s3 from "aws-cdk-lib/aws-s3";
import { Construct } from "constructs";

export interface S3ListerProps {
  env: string;
  inboundBucket: s3.IBucket;
}

/**
 * S3ListerConstruct — Lambda + HTTP API Gateway for Grafana JSON data source.
 *
 * Purpose: provides Dashboard 3 (File Manifest) with a live S3 object browser panel
 * showing inbound-raw/ and processed/ objects with exact file size and PGP confirmation.
 *
 * Architecture:
 *   Grafana → HTTP API (unauthenticated, VPC-internal would require PrivateLink; open for DEV)
 *   → Lambda (Python 3.12) → S3 ListObjectsV2 on inbound bucket
 *
 * Security posture (DEV only — lock down before TST/PRD):
 *   - API is public (no auth) — acceptable for DEV; add IAM auth or VPC endpoint before TST
 *   - Lambda role: S3 ListBucket + GetObjectAttributes on inbound bucket only
 *   - No VPC attachment needed (Lambda calls S3 via public endpoint; no PHI in responses)
 *
 * Response schema (Grafana JSON data source — table mode):
 *   POST / with body { "prefix": "inbound-raw/2026/03/20/" }
 *   → { "columns": [...], "rows": [[...], ...] }
 *
 * DEV limitation: pagination capped at 1000 objects (S3 single-page limit).
 * For PRD with high file volume, add ContinuationToken loop.
 */
export class S3ListerConstruct extends Construct {
  public readonly apiUrl: string;

  constructor(scope: Construct, id: string, props: S3ListerProps) {
    super(scope, id);

    const { env, inboundBucket } = props;

    // ── Lambda execution role ─────────────────────────────────────────────
    const listerRole = new iam.Role(this, "ListerRole", {
      roleName: `onefintech-${env}-s3-lister-role`,
      assumedBy: new iam.ServicePrincipal("lambda.amazonaws.com"),
      managedPolicies: [
        iam.ManagedPolicy.fromAwsManagedPolicyName("service-role/AWSLambdaBasicExecutionRole"),
      ],
    });

    // S3 read-only on inbound bucket — list + head only, no GetObject (no data exfil)
    listerRole.addToPolicy(new iam.PolicyStatement({
      sid: "S3ListInbound",
      actions: [
        "s3:ListBucket",
        "s3:GetObjectAttributes",
      ],
      resources: [
        inboundBucket.bucketArn,
        `${inboundBucket.bucketArn}/*`,
      ],
    }));

    // ── Lambda log group ──────────────────────────────────────────────────
    const logGroup = new logs.LogGroup(this, "ListerLogGroup", {
      logGroupName: `/onefintech/${env}/s3-lister`,
      retention: logs.RetentionDays.ONE_WEEK,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // ── Lambda function (inline Python) ───────────────────────────────────
    const listerFn = new lambda.Function(this, "ListerFn", {
      functionName: `onefintech-${env}-s3-lister`,
      runtime: lambda.Runtime.PYTHON_3_12,
      handler: "index.handler",
      role: listerRole,
      timeout: cdk.Duration.seconds(10),
      memorySize: 128,
      environment: {
        BUCKET_NAME: inboundBucket.bucketName,
      },
      logGroup,
      code: lambda.Code.fromInline(`
import json
import os
import boto3
from datetime import datetime, timezone

s3 = boto3.client("s3")
BUCKET = os.environ["BUCKET_NAME"]

# Allowed prefixes — whitelist to prevent arbitrary bucket scanning
ALLOWED_PREFIXES = ("inbound-raw/", "processed/", "")


def handler(event, context):
    headers = {
        "Content-Type": "application/json",
        "Access-Control-Allow-Origin": "*",
        "Access-Control-Allow-Headers": "Content-Type",
        "Access-Control-Allow-Methods": "GET,POST,OPTIONS",
    }

    # CORS preflight
    if event.get("requestContext", {}).get("http", {}).get("method") == "OPTIONS":
        return {"statusCode": 200, "headers": headers, "body": ""}

    # Parse request body
    try:
        body = json.loads(event.get("body") or "{}")
    except Exception:
        body = {}

    prefix = body.get("prefix", "inbound-raw/")
    if not any(prefix.startswith(p) for p in ALLOWED_PREFIXES):
        return {
            "statusCode": 400,
            "headers": headers,
            "body": json.dumps({"error": f"prefix not allowed: {prefix}"}),
        }

    # List S3 objects
    try:
        paginator = s3.get_paginator("list_objects_v2")
        rows = []
        for page in paginator.paginate(Bucket=BUCKET, Prefix=prefix, PaginationConfig={"MaxItems": 1000}):
            for obj in page.get("Contents", []):
                key = obj["Key"]
                filename = key.split("/")[-1]
                if not filename:  # skip directory-like keys
                    continue
                size_kb = round(obj["Size"] / 1024, 1)
                last_modified = obj["LastModified"].isoformat()
                # Infer PGP status from file extension
                pgp_confirmed = "Yes" if key.endswith(".pgp") or ".pgp." in key else "No"
                rows.append([key, filename, size_kb, last_modified, pgp_confirmed])
    except Exception as e:
        return {
            "statusCode": 500,
            "headers": headers,
            "body": json.dumps({"error": str(e)}),
        }

    # Grafana JSON data source table format
    response_body = {
        "columns": [
            {"text": "S3 Key",         "type": "string"},
            {"text": "Filename",       "type": "string"},
            {"text": "Size (KB)",      "type": "number"},
            {"text": "Last Modified",  "type": "time"},
            {"text": "PGP Encrypted",  "type": "string"},
        ],
        "rows": rows,
        "type": "table",
    }

    return {
        "statusCode": 200,
        "headers": headers,
        "body": json.dumps(response_body),
    }
`),
    });

    // ── HTTP API Gateway ──────────────────────────────────────────────────
    const httpApi = new apigwv2.HttpApi(this, "ListerApi", {
      apiName: `onefintech-${env}-s3-lister`,
      description: "S3 file browser for Grafana Dashboard 3 (File Manifest)",
      corsPreflight: {
        allowHeaders: ["Content-Type"],
        allowMethods: [apigwv2.CorsHttpMethod.GET, apigwv2.CorsHttpMethod.POST, apigwv2.CorsHttpMethod.OPTIONS],
        allowOrigins: ["*"], // DEV only — restrict to Grafana ALB DNS before TST
      },
    });

    const integration = new apigwv2integrations.HttpLambdaIntegration("ListerIntegration", listerFn);

    httpApi.addRoutes({
      path: "/",
      methods: [apigwv2.HttpMethod.POST, apigwv2.HttpMethod.GET],
      integration,
    });

    this.apiUrl = httpApi.apiEndpoint;

    new cdk.CfnOutput(scope, "S3ListerApiUrl", {
      value: this.apiUrl,
      description: "S3 Lister API URL — configure as Grafana JSON data source",
    });

    cdk.Tags.of(this).add("Project", "OneFintechFIS");
    cdk.Tags.of(this).add("Environment", env);
  }
}
