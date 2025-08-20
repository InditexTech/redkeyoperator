# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash

# Run all the shell e2e tests

# Exit on errors
set -o errexit
set -o pipefail
set -o nounset
# Scripts dir
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

source $SCRIPT_DIR/lib-test.sh

# Add test to this list
function test_list {
    # t <TIMEOUT> <SCRIPT> <PARAMETERS>
    # Testing known bugs
    t $minutes_5 test-migrating-in-the-air.sh 3 redis-operator redis-cluster-test
    t $minutes_5 test-slot-busy.sh 3 redis-operator redis-cluster-test
    t $minutes_5 test-with-a-master-becoming-a-slave.sh 3 redis-operator redis-cluster-test
    t $minutes_14 test-size-change.sh 3 redis-operator redis-cluster-test
    t $minutes_19 test-template-change.sh 3 redis-operator redis-cluster-test
    # Does the cluster scale before the timeout?
    t $minutes_14 test-scaling-up-and-delete.sh 21 redis-operator redis-cluster-test
    t $minutes_10 test-scaling-up-and-delete.sh 25 redis-operator redis-cluster-test
    t $minutes_10 test-scaling-up-and-delete.sh 28 redis-operator redis-cluster-test
    # Does the cluster scale with load before the timeout?
    t $minutes_6 test-scaling-up-with-load.sh 5 redis-operator redis-cluster-test
    t $minutes_11 test-scaling-up-with-load.sh 15 redis-operator redis-cluster-test
    # Chaos and timeout - Does minimum a cycle before the timeout
    t $minutes_60 test-chaos.sh 15 redis-operator redis-cluster-test 45
    t $minutes_60 test-chaos.sh 25 redis-operator redis-cluster-test 45
    t $minutes_60 test-chaos.sh 35 redis-operator redis-cluster-test 45
    t $minutes_60 test-chaos.sh 50 redis-operator redis-cluster-test 45
}

# Execute tests with TIMEOUT
function t {
    log_info "TESTING... $@" | tee -a target/test.out
    seconds="$1"
    script="$2"
    parameters=("${@:3}")
    stdout=target/$(basename $script | cut -d. -f1).out
    counter=0
    while [[ -e "$stdout" ]]; do
        counter=$(( counter + 1 ))
        stdout=target/$(basename $script | cut -d. -f1)-$counter.out
    done
    code=0
    timeout $seconds scripts/$script ${parameters[@]} > $stdout 2>&1 || code=$?
    if [[ $code == "0" ]]; then
        log_info  "TEST-OK $@" | tee -a target/test.out
    elif [[ $code == "124" ]]; then
        log_error "TEST-FAILED TIMEOUT $@" | tee -a target/test.out
    else
        log_error "TEST-FAILED ERRCODE=$code $@" | tee -a target/test.out
    fi
}

ensure_rediscluster
ensure_namespace
mkdir -p target

minutes_5=300
minutes_6=360
minutes_8=480
minutes_10=600
minutes_11=660
minutes_14=840
minutes_19=1140
minutes_60=3600

log_info "Test starting" | tee target/test.out

# Executing tests
test_list

log_info "Test ending" | tee -a target/test.out

tests=$(cat target/test.out | grep TESTING\\.\\.\\. | wc -l || true)
tests_ok=$(cat target/test.out | grep TEST-OK | wc -l || true)
tests_failed=$(cat target/test.out | grep TEST-FAILED | wc -l || true)

echo
echo "=================="
echo "SUMMARY: "
echo "=================="
echo Tests: $tests OK: $tests_ok FAILED: $tests_failed
if (( $test_failed > 0 )); then
    echo "=================="
    echo "FAILED: "
    echo "=================="
    cat target/test.out | grep TEST-FAILED
    echo "=================="
    exit 1
fi