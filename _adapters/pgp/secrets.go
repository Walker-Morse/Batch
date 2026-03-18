package pgp

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// getSecret fetches a plaintext secret value from AWS Secrets Manager.
// Returns the SecretString value trimmed of surrounding whitespace.
func getSecret(ctx context.Context, sm *secretsmanager.Client, arn string) (string, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(arn),
	})
	if err != nil {
		return "", fmt.Errorf("secretsmanager.GetSecretValue(%s): %w", arn, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secretsmanager.GetSecretValue(%s): SecretString is nil (binary secrets not supported)", arn)
	}
	return *out.SecretString, nil
}
