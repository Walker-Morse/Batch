#!/usr/bin/env node
import "source-map-support/register";
import * as cdk from "aws-cdk-lib";
import { OneFintechStack } from "../lib/stacks/onefintech-stack";

const app = new cdk.App();

// ── DEV ──────────────────────────────────────────────────────────────────────
// TODO: replace PLACEHOLDER_ACCOUNT_ID with real DEV AWS account ID before first deploy
new OneFintechStack(app, "OneFintechDev", {
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT ?? "PLACEHOLDER_ACCOUNT_ID",
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
  description: "One Fintech / FIS Prepaid Sunrise — DEV environment",
});

app.synth();
