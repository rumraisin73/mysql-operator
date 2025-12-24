package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func main() {

	dbHost := "test-cluster-svc-master.default"
	dbPort := "3306"
	dbUser := "root"
	dbPass := "root111"
	dbName := "test_write"

	dsnBase := fmt.Sprintf("%s:%s@tcp(%s:%s)/", dbUser, dbPass, dbHost, dbPort)
	// 用于连接mysql库
	dsnBootstrap := dsnBase + "mysql?timeout=2s"
	// 用于连接写测试库
	dsnWork := dsnBase + dbName + "?timeout=1s&readTimeout=1s&writeTimeout=1s"

	log.Printf("准备连接主库服务: %s:%s", dbHost, dbPort)

	// 确保写测试库存在
	for {
		dbBoot, err := sql.Open("mysql", dsnBootstrap)
		if err == nil {
			_, err = dbBoot.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", dbName))
			if err == nil {
				dbBoot.Close()
				log.Printf("数据库%s已就绪", dbName)
				break
			}
			dbBoot.Close()
		}
		log.Printf("等待数据库连通: %v", err)
		time.Sleep(2 * time.Second)
	}

	// 连接写测试库并初始化表
	db, err := sql.Open("mysql", dsnWork)
	if err != nil {
		log.Printf("写测试库配置错误: %v", err)
		return
	}
	defer db.Close()

	initDB(db)

	// 获取断点序号
	var seqNo int
	_ = db.QueryRow("SELECT IFNULL(MAX(seq_no), 0) FROM heartbeat").Scan(&seqNo)
	seqNo++
	log.Printf("测试启动，从序号SEQ %d 开始持续写入", seqNo)

	// 持续写入
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		startTime := time.Now()

		var hostname string
		// 查询@@hostname可以直观看到当前是哪个pod在处理写入
		_ = db.QueryRowContext(ctx, "SELECT @@hostname").Scan(&hostname)

		// 插入数据
		_, err := db.ExecContext(ctx,
			"INSERT INTO heartbeat (seq_no, pod_ip) VALUES (?, ?)",
			seqNo, hostname)

		duration := time.Since(startTime)
		cancel()

		if err != nil {

			log.Printf("SEQ %d 写入失败(数据丢失) | 耗时: %v | 错误: %v", seqNo, duration, err)
		} else {

			log.Printf("SEQ %d 写入成功 | 耗时: %v | 目标pod: %s", seqNo, duration, hostname)
		}

		// 论成功失败，序号自增，用于检测故障期
		seqNo++
		time.Sleep(1 * time.Second)
	}
}

func initDB(db *sql.DB) {
	query := `CREATE TABLE IF NOT EXISTS heartbeat (
                id INT AUTO_INCREMENT PRIMARY KEY,
                seq_no INT NOT NULL,
                write_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                pod_ip VARCHAR(64)
        )`
	for {
		_, err := db.Exec(query)
		if err == nil {
			log.Println("写测试表heartbeat已就绪")
			break
		}
		log.Printf("等待建表: %v", err)
		time.Sleep(2 * time.Second)
	}
}
