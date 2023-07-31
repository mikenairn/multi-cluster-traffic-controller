#!/bin/bash

TOOLS_IMAGE=quay.io/kuadrant/mgc-tools:latest

docker build . -t ${TOOLS_IMAGE} -f ./Dockerfile.tools

docker run --rm "${TOOLS_IMAGE}" -c 'kustomize version'
docker run --rm "${TOOLS_IMAGE}" -c 'operator-sdk version'
docker run --rm "${TOOLS_IMAGE}" -c 'kind --version'
docker run --rm "${TOOLS_IMAGE}" -c 'helm version'
docker run --rm "${TOOLS_IMAGE}" -c 'yq --version'
docker run --rm "${TOOLS_IMAGE}" -c 'istioctl version'
docker run --network=host -u $UID -v "${HOME}:/home:z" --rm "${TOOLS_IMAGE}" -c 'clusteradm version'

## Need home here in order to pick up on host kubeconfig
docker run -v "${HOME}:/home:z" --rm "${TOOLS_IMAGE}" -c 'kustomize build github.com/Kuadrant/multicluster-gateway-controller/config/cert-manager?ref=main --enable-helm --helm-command helm'

## Need network=host here so we can access kind clusters using localhost in host kubeconfig
docker run --network=host -u $UID -v "${HOME}:/home:z" --rm "${TOOLS_IMAGE}" -c 'operator-sdk olm status'
