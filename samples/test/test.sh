#!/bin/bash

SCRIPT_DIRECTORY="$(dirname $(realpath "$0"))"

$SCRIPT_DIRECTORY/test_cleanup.sh

sleep 10

# OCM setup
kubectl label managedcluster kind-mgc-control-plane ingress-cluster=true
kubectl label managedcluster kind-mgc-workload-1 ingress-cluster=true
kubectl label managedcluster kind-mgc-workload-2 ingress-cluster=true

kubectl apply -f samples/test/managed-cluster-set_gateway-clusters.yaml
kubectl apply -f samples/test/managed-cluster-set-binding_gateway-clusters.yaml
kubectl apply -f samples/test/placement_http-gateway.yaml
kubectl create -f hack/ocm/gatewayclass.yaml

# Create gateway (myapp.mn.hcpapps.net)
kubectl apply -f samples/test/gateway_prod-web.yaml
kubectl label gateway prod-web "cluster.open-cluster-management.io/placement"="http-gateway" -n multi-cluster-gateways

sleep 2

# Deploy echo app to mgc-control-plane, mgc-workload-1 and mgc-workload-2
kubectl --context kind-mgc-control-plane apply -f samples/test/echo-app.yaml
kubectl --context kind-mgc-workload-1 apply -f samples/test/echo-app.yaml
kubectl --context kind-mgc-workload-2 apply -f samples/test/echo-app.yaml

# Check gateways
kubectl --context kind-mgc-control-plane get gateways -A
kubectl --context kind-mgc-workload-1 get gateways -A
kubectl --context kind-mgc-workload-2 get gateways -A

# Check dnspolicy
kubectl --context kind-mgc-control-plane get dnspolicy -n multi-cluster-gateways



kubectl create secret generic mgc-aws-credentials --type kuadrant.io/aws --from-env-file=aws-credentials.env -n multi-cluster-gateways