# End to End Test for Kubernetes RedKey Operator

## Purpose

End to End tests for provision Redis cluster environments in Kubernetes or Openshift.

## How to run the test

### Prerequisites
1. make
2. kubectl with configured access to create CRD, namespaces, deployments
3. kubernetes cluster
4. go

## Deploying the kubernetes cluster locally

In a new terminal session execute this script file

```
bash ./test/cmd/check_cluster_test.sh
``` 

The script file creates a new kubernetes cluster if this one not exists

## Execute the end to end tests

In the terminal execute this make command

```
make test-e2e-cov
```

The command creates a new instance for the RedKey operator and continues to run the available test

## Clean environment

In the terminal execute this script file

```
./test/cmd/clean.sh
```

the script file clean the environment including the kubernetes cluster