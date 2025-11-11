BIN ?= build/gocql-observer
IMAGE ?= gocql-observer:latest
INTEGRATION_LOG_DIR ?= ./logs/integration
SCYLLA_IMAGE ?= scylladb/scylla

.PHONY: all build image integration-test clean

all: build image

build:
	go build -o $(BIN) .

image:
	docker build -t $(IMAGE) .

test: build
	SCYLLA_IMAGE=$(SCYLLA_IMAGE) docker-compose -f tests/docker-compose.yml up -d --wait
	CLUSTER1_CONTACT_POINTS="192.168.10.10" CLUSTER1_CONTACT_POINTS="192.168.10.10" LOG_DIRECTORY="$(INTEGRATION_LOG_DIR)" RUN_DURATION=1m build/gicql-observer

clean:
	rm -f $(BIN)
