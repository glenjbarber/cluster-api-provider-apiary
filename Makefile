CONTROLLER_GEN_VERSION ?= v0.16.5

.PHONY: build
build:
	go build -o bin/manager ./cmd

.PHONY: test
test:
	go build ./...
	go vet ./...
	go test ./...

.PHONY: generate
generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object:headerFile="" paths="./api/..."

.PHONY: manifests
manifests:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) rbac:roleName=manager-role paths="./internal/controller/..." output:rbac:artifacts:config=config/rbac

.PHONY: deploy
deploy:
	kubectl apply -k config/default
