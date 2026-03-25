#!/usr/bin/env node
import "source-map-support/register";
import * as cdk from "aws-cdk-lib";
import { OneFintechStack } from "../lib/stacks/onefintech-stack";

const app = new cdk.App();

// ── DEV ──────────────────────────────────────────────────────────────────────
// TODO: replace 307871782435 with real DEV AWS account ID before first deploy
new OneFintechStack(app, "OneFintechDev", {
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT ?? "307871782435",
    region: process.env.CDK_DEFAULT_REGION ?? "us-east-1",
  },
  environment: "dev",
  // DEV: min 0.5 ACU allows scale-to-zero; max 4 ACU sufficient for DEV loads (ADR-008)
  auroraMinAcu: 0.5,
  auroraMaxAcu: 4,
  // TODO: replace PLACEHOL with real FIS company ID from Selvi Marappan (Open Item #31)
  fisCompanyId: "PLACEHOL",
  // DEV: nightly 02:00 UTC — confirm submission window with Kendra Williams (Open Item #29)
  scheduleCron: "cron(0 2 * * ? *)",
  // DEV: single NAT gateway (cost optimised); TST/PRD: one per AZ
  natGateways: 1,
  // PGP key Secrets Manager ARNs.
  // Empty strings = NullPGP passthrough — allowed in DEV only (task enforces at startup).
  // Provision secrets and populate ARNs before TST deploy (Open Item #41).
  pgpPrivateKeySecretArn: process.env.PGP_PRIVATE_KEY_SECRET_ARN ?? "",
  pgpPassphraseSecretArn: process.env.PGP_PASSPHRASE_SECRET_ARN ?? "",
  pgpFisPublicKeySecretArn: process.env.PGP_FIS_PUBLIC_KEY_SECRET_ARN ?? "",
  // Git SHA of the api-server image to deploy. Set by CI via API_SERVER_IMAGE_TAG.
  // Pinning to a SHA forces a new task definition revision on every image push,
  // ensuring ECS always deploys the latest build. Falls back to "latest" for local synth.
  apiServerImageTag: process.env.API_SERVER_IMAGE_TAG ?? "latest",
  description: "One Fintech / FIS Prepaid Sunrise — DEV environment",
});

app.synth();
