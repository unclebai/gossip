# !/bin/sh
instance=$1
if [ "$instance"x == ""x ];then
    echo "Instance parameter is missing."
    exit 1
fi

go build ./
docker build ./ -t gossip:latest

sed "s/{{INSTANCE}}/${instance}/"  ./docker.yaml > /tmp/gossip_${instance}.yaml
docker network create gossip_network --subnet=10.20.19.0/24

docker-compose -f /tmp/gossip_${instance}.yaml up &
