#!/bin/sh
curl -s http://localhost:9091/metrics | grep 9099 | grep -i bytes
curl -s http://localhost:9092/metrics | grep 9099 | grep -i bytes
