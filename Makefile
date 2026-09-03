CONTROLLER_GEN_VERSION ?= v0.16.5
KUSTOMIZE_VERSION ?= v5.8.1

# IMG is the image reference docker-build/docker-push/build-installer all
# use - override on the command line for a real release, e.g.:
#   make docker-build docker-push IMG=ghcr.io/glenjbarber/cluster-api-provider-apiary:v0.1.0
IMG ?= ghcr.io/glenjbarber/cluster-api-provider-apiary:dev

.PHONY: build
build:
	go build -o bin/manager ./cmd

.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push:
	docker push $(IMG)

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

# build-installer renders config/default with IMG pinned in, producing
# the single-file manifest bundle clusterctl expects as a GitHub Release
# asset (infrastructure-components.yaml, alongside metadata.yaml at the
# repo root - see the Cluster API book's "provider contract"). Reverts
# config/manager's own kustomization.yaml back to the "controller:latest"
# placeholder afterward, so a real release build doesn't leave a stray
# real image tag committed to the working tree.
.PHONY: build-installer
build-installer:
	mkdir -p out
	cd config/manager && go run sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION) edit set image controller=$(IMG)
	go run sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION) build config/default > out/infrastructure-components.yaml
	cd config/manager && go run sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION) edit set image controller=controller:latest
