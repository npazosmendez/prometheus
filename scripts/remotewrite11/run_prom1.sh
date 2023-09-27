#!/bin/sh
PROM_HOME=`pwd`
./prometheus --config.file=$PROM_HOME/prom1.yml --web.listen-address="0.0.0.0:9091" --storage.tsdb.path="$PROM_HOME/prom1/data/"
