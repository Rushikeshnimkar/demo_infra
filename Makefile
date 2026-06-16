# Build directory lives in WSL-native /tmp to avoid NTFS permission and
# upload-stall issues that occur when zipping/uploading from /mnt/d/.

GOOS      = linux
GOARCH    = amd64
BUILD_DIR = /tmp/pms-build

.PHONY: build build-api build-trigger upload clean tf-init tf-plan deploy destroy seed

build: build-api build-trigger

build-api:
	mkdir -p $(BUILD_DIR)/api
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		go build -tags lambda.norpc -ldflags="-s -w" \
		-o $(BUILD_DIR)/api/bootstrap ./lambdas/api
	cd $(BUILD_DIR)/api && zip -j api.zip bootstrap
	@echo "api.zip: $$(du -sh $(BUILD_DIR)/api/api.zip | cut -f1)"

build-trigger:
	mkdir -p $(BUILD_DIR)/trigger
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		go build -tags lambda.norpc -ldflags="-s -w" \
		-o $(BUILD_DIR)/trigger/bootstrap ./lambdas/trigger
	cd $(BUILD_DIR)/trigger && zip -j trigger.zip bootstrap
	@echo "trigger.zip: $$(du -sh $(BUILD_DIR)/trigger/trigger.zip | cut -f1)"

upload:
	$(eval BUCKET := $(shell cd terraform && terraform output -raw lambda_bucket))
	aws configure set default.s3.multipart_threshold 1MB
	aws configure set default.s3.multipart_chunksize 1MB
	aws s3 cp $(BUILD_DIR)/api/api.zip     s3://$(BUCKET)/api.zip
	aws s3 cp $(BUILD_DIR)/trigger/trigger.zip s3://$(BUCKET)/trigger.zip
	@echo "Uploaded to s3://$(BUCKET)"

clean:
	rm -rf $(BUILD_DIR)

tf-init:
	cd terraform && terraform init

tf-plan: build
	cd terraform && terraform plan

deploy: build
	cd terraform && terraform apply -target=aws_s3_bucket.lambda_artifacts \
	                                -target=aws_s3_bucket_public_access_block.lambda_artifacts \
	                                -auto-approve
	$(MAKE) upload
	cd terraform && terraform apply -auto-approve

destroy:
	cd terraform && terraform destroy -auto-approve

seed:
	API_URL=$(API_URL) go run ./scripts/seed
