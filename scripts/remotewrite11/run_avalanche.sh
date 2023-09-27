#!/bin/sh

./avalanche --port=9093 --metric-count=500 --label-count=10 --series-count=10 --metricname-length=5 --labelname-length=5 --const-label=cluster=dev-eu-central-0 --value-interval=30 --series-interval=60 --metric-interval=120
