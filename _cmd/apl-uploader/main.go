// Command apl-uploader generates and uploads the Approved Products List to FIS.
//
// Triggered by EventBridge Scheduler on the first business day of each month (§B.5).
// Compute ownership decision required before APL implementation sprint (Open Item #11, Apr 7 target).
//
// What it does:
//   1. Reads apl_rules for the target program + benefit_type
//   2. Generates fixed-width FIS APL batch file (same format as card management files)
//   3. PGP-encrypts with FIS public key
//   4. Deletes plaintext after encryption
//   5. Delivers via AWS Transfer Family to FIS SFTP
//   6. Writes new apl_versions row on success
//   7. Atomically updates apl_rules.active_version_id
//
// RFU Phase 1: two APLs loaded monthly (BRD 3/6/2026):
//   RFUORFVM  — Purse 1 (Fruits & Veg), category 5D only
//   RFUORPANM — Purse 2 (Pantry), categories 5D + 5K
//
// APL process is manual on FIS side (email with FIS EBT team).
// This command automates the Morse side. (Kendra Williams / Kyle Walker, Open Items #7, #11)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	tenantID    := flag.String("tenant-id", "", "health plan client tenant ID")
	programID   := flag.String("program-id", "", "program UUID")
	benefitType := flag.String("benefit-type", "", "OTC|FOD|CMB")
	dryRun      := flag.Bool("dry-run", false, "generate APL file without uploading to FIS")
	flag.Parse()

	if *tenantID == "" || *programID == "" || *benefitType == "" {
		fmt.Fprintln(os.Stderr, "error: --tenant-id, --program-id, and --benefit-type are required")
		flag.Usage()
		os.Exit(1)
	}

	log.Printf("apl-uploader: tenant=%s program=%s benefit_type=%s dry_run=%v",
		*tenantID, *programID, *benefitType, *dryRun)

	// TODO: read apl_rules from Aurora
	// TODO: generate fixed-width APL batch file
	// TODO: PGP-encrypt with FIS public key from Secrets Manager
	// TODO: delete plaintext immediately after encryption
	// TODO: deliver via AWS Transfer Family
	// TODO: write apl_versions row + update apl_rules.active_version_id
}
