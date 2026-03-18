module github.com/walker-morse/batch

go 1.23

require (
	github.com/aws/aws-sdk-go-v2 v1.30.0
	github.com/aws/aws-sdk-go-v2/config v1.27.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.58.0
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.32.0
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.33.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	go.opentelemetry.io/otel v1.28.0
	go.opentelemetry.io/otel/trace v1.28.0
	go.uber.org/zap v1.27.0
)
