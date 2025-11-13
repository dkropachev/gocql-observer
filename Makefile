SHELL=bash
.ONE_SHELL:
BIN ?= build/gocql-observer
IMAGE ?= gocql-observer:latest
INTEGRATION_LOG_DIR ?= ./logs/integration
SCYLLA_IMAGE ?= scylladb/scylla

.PHONY: all build image integration-test clean

all: build image

build:
	go build -o $(BIN) .

build-image:
	docker build -t $(IMAGE) .

test-docker: build-image .fix-aio-max-nr
	SCYLLA_IMAGE=$(SCYLLA_IMAGE) docker-compose -f tests/docker-compose.yml up -d --wait

test-build: build
	echo export CLUSTER1_CONTACT_POINTS=`docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' tests-scylla-cluster1-1`
	export CLUSTER1_CONTACT_POINTS=`docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' tests-scylla-cluster1-1`
	export CLUSTER2_CONTACT_POINTS=`docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' tests-scylla-cluster1-2`
	echo ${CLUSTER1_CONTACT_POINTS}
	LOG_DIRECTORY="$(INTEGRATION_LOG_DIR)" RUN_DURATION=1m build/gocql-observer


clean:
	rm -f $(BIN)

.fix-aio-max-nr:
	@if [ `sysctl -n fs.aio-max-nr` -lt 10485760 ]; then \
		sudo sysctl -w fs.aio-max-nr=10485760; \
	fi