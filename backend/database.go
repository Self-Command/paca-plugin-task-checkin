package main

import plugin "github.com/Paca-AI/plugin-sdk-go"

// Paca uses one PostgreSQL pool with transaction-local search paths. A unique
// stable SQL prefix prevents prepared-plan reuse across independent schemas.
type scopedDB struct{ *plugin.DB }

func (db *scopedDB) Query(sql string, params ...any) (*plugin.DBQueryResult, error) {
	return db.DB.Query("/* "+pluginID+" */ "+sql, params...)
}
func (db *scopedDB) Exec(sql string, params ...any) (int64, error) {
	return db.DB.Exec("/* "+pluginID+" */ "+sql, params...)
}
