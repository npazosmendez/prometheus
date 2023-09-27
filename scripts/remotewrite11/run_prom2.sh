#!/bin/sh
PROM_HOME=`pwd`
./prometheus.callum --config.file=$PROM_HOME/prom2.yml --web.listen-address="0.0.0.0:9092" --storage.tsdb.path="$PROM_HOME/prom2/data/" --enable-feature reduced-rw-proto
