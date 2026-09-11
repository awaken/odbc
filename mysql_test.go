//go:build odbc_integration

// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql"
	"flag"
	"os"
	"testing"
	"time"
)

var (
	mysrv  = flag.String("mysrv", "server", "mysql server name")
	mydb   = flag.String("mydb", "dbname", "mysql database name")
	myuser = flag.String("myuser", "", "mysql user name")
	mypass = flag.String("mypass", os.Getenv("ODBC_MYSQL_PASSWORD"), "mysql password")
)

func mysqlConnect() (db *sql.DB, stmtCount int, err error) {
	// from https://dev.mysql.com/doc/connector-odbc/en/connector-odbc-configuration-connection-parameters.html
	conn := mysqlConnectionString(*mysrv, *mydb, *myuser, *mypass)
	db, err = sql.Open("odbc", conn)
	if err != nil {
		return nil, 0, err
	}
	stats := db.Driver().(*Driver).Stats()
	return db, stats.StmtCount, nil
}

func mysqlConnectionString(server, database, user, password string) string {
	return makeODBCAttributes(connParams{"driver": "mysql", "server": server, "database": database, "user": user, "password": password})
}

func TestMySQLSyntheticConnectionString(t *testing.T) {
	got := mysqlConnectionString("local", "db;name", " test ", "a}b;c")
	want := "database={db;name};driver={mysql};password={a}}b;c};server={local};user={ test };"
	if got != want {
		t.Fatalf("synthetic DSN = %q; want %q", got, want)
	}
}

func TestMYSQLTime(t *testing.T) {
	db, sc, err := mysqlConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table temp")
	exec(t, db, "create table temp(id int not null auto_increment primary key, time time)")
	now := time.Now()
	// SQL_TIME_STRUCT only supports hours, minutes and seconds
	now = time.Date(1, time.January, 1, now.Hour(), now.Minute(), now.Second(), 0, time.Local)
	_, err = db.Exec("insert into temp (time) values(?)", now)
	if err != nil {
		t.Fatal(err)
	}

	var ret time.Time
	if err := db.QueryRow("select time from temp where id = ?", 1).Scan(&ret); err != nil {
		t.Fatal(err)
	}
	if ret != now {
		t.Fatalf("unexpected return value: want=%v, is=%v", now, ret)
	}

	exec(t, db, "drop table temp")
}
