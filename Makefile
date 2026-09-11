
DB_NAME=test
ODBC_READY_TIMEOUT?=120
ODBC_COMMAND_TIMEOUT?=10
ODBC_DIR:=$(dir $(abspath $(lastword $(MAKEFILE_LIST))))
export DB_NAME ODBC_READY_TIMEOUT ODBC_COMMAND_TIMEOUT
PASSWORD=Passw0rd

help:
	echo "use start or stop target"

# Microsoft SQL Server

MSSQL_CONTAINER_NAME=mssql_test
MSSQL_SA_PASSWORD=$(PASSWORD)
MSSQL_NETWORK=mssqlnetwork
MSSQL_DRIVER_NAME=ODBC Driver 18 for SQL Server
MSSQL_IMAGE=mcr.microsoft.com/mssql/server:2025-latest
ODBC_MSSQL_PASSWORD=$(MSSQL_SA_PASSWORD)
export MSSQL_SA_PASSWORD
export ODBC_MSSQL_PASSWORD

start-mssql:
	docker network create ${MSSQL_NETWORK}
	docker run \
		--name $(MSSQL_CONTAINER_NAME) \
		--hostname $(MSSQL_CONTAINER_NAME) \
		-e 'ACCEPT_EULA=Y' \
		-e MSSQL_SA_PASSWORD \
		-d \
		-p 1433:1433 \
		--network=${MSSQL_NETWORK} \
		$(MSSQL_IMAGE)
	@bash "$(ODBC_DIR)waitdb.sh" mssql "$(MSSQL_CONTAINER_NAME)"

build-unixodbc:
	docker build \
		-t unixodbc \
		-f unixodbc.Dockerfile \
		.

test-mssql:
	docker run \
		-it \
		--network=${MSSQL_NETWORK} \
		-e ODBC_MSSQL_PASSWORD \
		-v .:/src \
		unixodbc \
			sh /src/mssqltest.sh \
				"$(MSSQL_DRIVER_NAME)" \
				"$(MSSQL_CONTAINER_NAME)" \
				"$(DB_NAME)"

test-mssql-freetds:
	docker run \
		-it \
		--network=${MSSQL_NETWORK} \
		-e ODBC_MSSQL_PASSWORD \
		-v .:/src \
		unixodbc \
			sh /src/mssqltest.sh \
				"freetds" \
				"$(MSSQL_CONTAINER_NAME)" \
				"$(DB_NAME)"


test-mssql-race:
	docker run \
		-it \
		--network=${MSSQL_NETWORK} \
		-e ODBC_MSSQL_PASSWORD \
		-v .:/src \
		unixodbc \
			sh /src/mssqltest.sh \
				"$(MSSQL_DRIVER_NAME)" \
				"$(MSSQL_CONTAINER_NAME)" \
				"$(DB_NAME)" \
				"" \
				"--race"

stop-mssql:
	docker stop $(MSSQL_CONTAINER_NAME)
	docker rm $(MSSQL_CONTAINER_NAME)
	docker network rm ${MSSQL_NETWORK}

# MySQL

MYSQL_CONTAINER_NAME=mysql_test
MYSQL_ROOT_PASSWORD=$(PASSWORD)
MYSQL_IMAGE=mysql:9.7.2
ODBC_MYSQL_PASSWORD=$(MYSQL_ROOT_PASSWORD)
export MYSQL_ROOT_PASSWORD
export ODBC_MYSQL_PASSWORD

start-mysql:
	docker run --name=$(MYSQL_CONTAINER_NAME) -e MYSQL_ROOT_PASSWORD -d -p 127.0.0.1:3306:3306 $(MYSQL_IMAGE)
	@bash "$(ODBC_DIR)waitdb.sh" mysql "$(MYSQL_CONTAINER_NAME)"

test-mysql:
	go test -tags=odbc_integration -v -mydb=$(DB_NAME) -mysrv=127.0.0.1 -myuser=root -run=MYSQL

stop-mysql:
	docker stop $(MYSQL_CONTAINER_NAME)
	docker rm $(MYSQL_CONTAINER_NAME)
