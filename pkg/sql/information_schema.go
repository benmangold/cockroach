// Copyright 2016 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package sql

import (
	"context"
	"sort"
	"strconv"

	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/sql/privilege"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sessiondata"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/sql/vtable"
	"github.com/pkg/errors"
)

const (
	informationSchemaName = "information_schema"
	pgCatalogName         = sessiondata.PgCatalogName
)

// informationSchema lists all the table definitions for
// information_schema.
var informationSchema = virtualSchema{
	name: informationSchemaName,
	allTableNames: buildStringSet(
		// Generated with:
		// select distinct '"'||table_name||'",' from information_schema.tables
		//    where table_schema='information_schema' order by table_name;
		"_pg_foreign_data_wrappers",
		"_pg_foreign_servers",
		"_pg_foreign_table_columns",
		"_pg_foreign_tables",
		"_pg_user_mappings",
		"administrable_role_authorizations",
		"applicable_roles",
		"attributes",
		"character_sets",
		"check_constraint_routine_usage",
		"check_constraints",
		"collation_character_set_applicability",
		"collations",
		"column_domain_usage",
		"column_options",
		"column_privileges",
		"column_udt_usage",
		"columns",
		"constraint_column_usage",
		"constraint_table_usage",
		"data_type_privileges",
		"domain_constraints",
		"domain_udt_usage",
		"domains",
		"element_types",
		"enabled_roles",
		"foreign_data_wrapper_options",
		"foreign_data_wrappers",
		"foreign_server_options",
		"foreign_servers",
		"foreign_table_options",
		"foreign_tables",
		"information_schema_catalog_name",
		"key_column_usage",
		"parameters",
		"referential_constraints",
		"role_column_grants",
		"role_routine_grants",
		"role_table_grants",
		"role_udt_grants",
		"role_usage_grants",
		"routine_privileges",
		"routines",
		"schemata",
		"sequences",
		"sql_features",
		"sql_implementation_info",
		"sql_languages",
		"sql_packages",
		"sql_parts",
		"sql_sizing",
		"sql_sizing_profiles",
		"table_constraints",
		"table_privileges",
		"tables",
		"transforms",
		"triggered_update_columns",
		"triggers",
		"udt_privileges",
		"usage_privileges",
		"user_defined_types",
		"user_mapping_options",
		"user_mappings",
		"view_column_usage",
		"view_routine_usage",
		"view_table_usage",
		"views",
	),
	tableDefs: []virtualSchemaDef{
		informationSchemaAdministrableRoleAuthorizations,
		informationSchemaApplicableRoles,
		informationSchemaColumnPrivileges,
		informationSchemaColumnsTable,
		informationSchemaConstraintColumnUsageTable,
		informationSchemaEnabledRoles,
		informationSchemaKeyColumnUsageTable,
		informationSchemaParametersTable,
		informationSchemaReferentialConstraintsTable,
		informationSchemaRoleTableGrants,
		informationSchemaRoutineTable,
		informationSchemaSchemataTable,
		informationSchemaSchemataTablePrivileges,
		informationSchemaSequences,
		informationSchemaStatisticsTable,
		informationSchemaTableConstraintTable,
		informationSchemaTablePrivileges,
		informationSchemaTablesTable,
		informationSchemaViewsTable,
		informationSchemaUserPrivileges,
	},
	tableValidator:             validateInformationSchemaTable,
	validWithNoDatabaseContext: true,
}

func buildStringSet(ss ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

var (
	// information_schema was defined before the BOOLEAN data type was added to
	// the SQL specification. Because of this, boolean values are represented as
	// STRINGs. The BOOLEAN data type should NEVER be used in information_schema
	// tables. Instead, define columns as STRINGs and map bools to STRINGs using
	// yesOrNoDatum.
	yesString = tree.NewDString("YES")
	noString  = tree.NewDString("NO")
)

func yesOrNoDatum(b bool) tree.Datum {
	if b {
		return yesString
	}
	return noString
}

func dNameOrNull(s string) tree.Datum {
	if s == "" {
		return tree.DNull
	}
	return tree.NewDName(s)
}

func dStringPtrOrEmpty(s *string) tree.Datum {
	if s == nil {
		return emptyString
	}
	return tree.NewDString(*s)
}

func dStringPtrOrNull(s *string) tree.Datum {
	if s == nil {
		return tree.DNull
	}
	return tree.NewDString(*s)
}

func dIntFnOrNull(fn func() (int32, bool)) tree.Datum {
	if n, ok := fn(); ok {
		return tree.NewDInt(tree.DInt(n))
	}
	return tree.DNull
}

func validateInformationSchemaTable(table *sqlbase.TableDescriptor) error {
	// Make sure no tables have boolean columns.
	for _, col := range table.Columns {
		if col.Type.SemanticType == sqlbase.ColumnType_BOOL {
			return errors.Errorf("information_schema tables should never use BOOL columns. "+
				"See the comment about yesOrNoDatum. Found BOOL column in %s.", table.Name)
		}
	}
	return nil
}

var informationSchemaAdministrableRoleAuthorizations = virtualSchemaTable{
	schema: vtable.InformationSchemaAdministrableRoleAuthorizations,
	populate: func(ctx context.Context, p *planner, _ *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		currentUser := p.SessionData().User
		memberMap, err := p.MemberOfWithAdminOption(ctx, currentUser)
		if err != nil {
			return err
		}

		grantee := tree.NewDString(currentUser)
		for roleName, isAdmin := range memberMap {
			if !isAdmin {
				// We only show memberships with the admin option.
				continue
			}

			if err := addRow(
				grantee,                   // grantee: always the current user
				tree.NewDString(roleName), // role_name
				yesString,                 // is_grantable: always YES
			); err != nil {
				return err
			}
		}

		return nil
	},
}

var informationSchemaApplicableRoles = virtualSchemaTable{
	schema: vtable.InformationSchemaApplicableRoles,
	populate: func(ctx context.Context, p *planner, _ *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		currentUser := p.SessionData().User
		memberMap, err := p.MemberOfWithAdminOption(ctx, currentUser)
		if err != nil {
			return err
		}

		grantee := tree.NewDString(currentUser)

		for roleName, isAdmin := range memberMap {
			if err := addRow(
				grantee,                   // grantee: always the current user
				tree.NewDString(roleName), // role_name
				yesOrNoDatum(isAdmin),     // is_grantable
			); err != nil {
				return err
			}
		}

		return nil
	},
}

var informationSchemaColumnPrivileges = virtualSchemaTable{
	schema: vtable.InformationSchemaColumnPrivileges,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDesc(ctx, p, dbContext, virtualMany, func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
			dbNameStr := tree.NewDString(db.Name)
			scNameStr := tree.NewDString(scName)
			columndata := privilege.List{privilege.SELECT, privilege.INSERT, privilege.UPDATE} // privileges for column level granularity
			for _, u := range table.Privileges.Users {
				for _, priv := range columndata {
					if priv.Mask()&u.Privileges != 0 {
						for _, cd := range table.Columns {
							if err := addRow(
								tree.DNull,                     // grantor
								tree.NewDString(u.User),        // grantee
								dbNameStr,                      // table_catalog
								scNameStr,                      // table_schema
								tree.NewDString(table.Name),    // table_name
								tree.NewDString(cd.Name),       // column_name
								tree.NewDString(priv.String()), // privilege_type
								tree.DNull,                     // is_grantable
							); err != nil {
								return err
							}
						}
					}
				}
			}
			return nil
		})
	},
}

var informationSchemaColumnsTable = virtualSchemaTable{
	schema: vtable.InformationSchemaColumns,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDesc(ctx, p, dbContext, virtualMany, func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
			dbNameStr := tree.NewDString(db.Name)
			scNameStr := tree.NewDString(scName)
			// Table descriptors already holds columns in-order.
			visible := 0
			return forEachColumnInTable(table, func(column *sqlbase.ColumnDescriptor) error {
				visible++
				return addRow(
					dbNameStr,                                                   // table_catalog
					scNameStr,                                                   // table_schema
					tree.NewDString(table.Name),                                 // table_name
					tree.NewDString(column.Name),                                // column_name
					tree.NewDInt(tree.DInt(visible)),                            // ordinal_position, 1-indexed
					dStringPtrOrNull(column.DefaultExpr),                        // column_default
					yesOrNoDatum(column.Nullable),                               // is_nullable
					tree.NewDString(column.Type.InformationSchemaVisibleType()), // data_type
					characterMaximumLength(column.Type),                         // character_maximum_length
					characterOctetLength(column.Type),                           // character_octet_length
					numericPrecision(column.Type),                               // numeric_precision
					numericPrecisionRadix(column.Type),                          // numeric_precision_radix
					numericScale(column.Type),                                   // numeric_scale
					datetimePrecision(column.Type),                              // datetime_precision
					tree.DNull,                                                  // character_set_catalog
					tree.DNull,                                                  // character_set_schema
					tree.DNull,                                                  // character_set_name
					dStringPtrOrEmpty(column.ComputeExpr),                       // generation_expression
					yesOrNoDatum(column.Hidden),                                 // is_hidden
					tree.NewDString(column.Type.SQLString()),                    // crdb_sql_type
				)
			})
		})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-enabled-roles.html
// MySQL:    missing
var informationSchemaEnabledRoles = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.enabled_roles (
	ROLE_NAME STRING NOT NULL
)`,
	populate: func(ctx context.Context, p *planner, _ *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		currentUser := p.SessionData().User
		memberMap, err := p.MemberOfWithAdminOption(ctx, currentUser)
		if err != nil {
			return err
		}

		// The current user is always listed.
		if err := addRow(
			tree.NewDString(currentUser), // role_name: the current user
		); err != nil {
			return err
		}

		for roleName := range memberMap {
			if err := addRow(
				tree.NewDString(roleName), // role_name
			); err != nil {
				return err
			}
		}

		return nil
	},
}

func characterMaximumLength(colType sqlbase.ColumnType) tree.Datum {
	return dIntFnOrNull(colType.MaxCharacterLength)
}

func characterOctetLength(colType sqlbase.ColumnType) tree.Datum {
	return dIntFnOrNull(colType.MaxOctetLength)
}

func numericPrecision(colType sqlbase.ColumnType) tree.Datum {
	return dIntFnOrNull(colType.NumericPrecision)
}

func numericPrecisionRadix(colType sqlbase.ColumnType) tree.Datum {
	return dIntFnOrNull(colType.NumericPrecisionRadix)
}

func numericScale(colType sqlbase.ColumnType) tree.Datum {
	return dIntFnOrNull(colType.NumericScale)
}

func datetimePrecision(colType sqlbase.ColumnType) tree.Datum {
	// We currently do not support a datetime precision.
	return tree.DNull
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-constraint-column-usage.html
// MySQL:    missing
var informationSchemaConstraintColumnUsageTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.constraint_column_usage (
	TABLE_CATALOG      STRING NOT NULL,
	TABLE_SCHEMA       STRING NOT NULL,
	TABLE_NAME         STRING NOT NULL,
	COLUMN_NAME        STRING NOT NULL,
	CONSTRAINT_CATALOG STRING NOT NULL,
	CONSTRAINT_SCHEMA  STRING NOT NULL,
	CONSTRAINT_NAME    STRING NOT NULL
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDescWithTableLookup(ctx, p, dbContext, hideVirtual /*no constraints in virtual tables*/, func(
			db *sqlbase.DatabaseDescriptor,
			scName string,
			table *sqlbase.TableDescriptor,
			tableLookup tableLookupFn,
		) error {
			conInfo, err := table.GetConstraintInfoWithLookup(tableLookup.getTableByID)
			if err != nil {
				return err
			}
			scNameStr := tree.NewDString(scName)
			dbNameStr := tree.NewDString(db.Name)

			for conName, con := range conInfo {
				conTable := table
				conCols := con.Columns
				conNameStr := tree.NewDString(conName)
				if con.Kind == sqlbase.ConstraintTypeFK {
					// For foreign key constraint, constraint_column_usage
					// identifies the table/columns that the foreign key
					// references.
					conTable = con.ReferencedTable
					conCols = con.ReferencedIndex.ColumnNames
				}
				tableNameStr := tree.NewDString(conTable.Name)
				for _, col := range conCols {
					if err := addRow(
						dbNameStr,            // table_catalog
						scNameStr,            // table_schema
						tableNameStr,         // table_name
						tree.NewDString(col), // column_name
						dbNameStr,            // constraint_catalog
						scNameStr,            // constraint_schema
						conNameStr,           // constraint_name
					); err != nil {
						return err
					}
				}
			}
			return nil
		})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-key-column-usage.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/key-column-usage-table.html
var informationSchemaKeyColumnUsageTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.key_column_usage (
	CONSTRAINT_CATALOG STRING NOT NULL,
	CONSTRAINT_SCHEMA  STRING NOT NULL,
	CONSTRAINT_NAME    STRING NOT NULL,
	TABLE_CATALOG      STRING NOT NULL,
	TABLE_SCHEMA       STRING NOT NULL,
	TABLE_NAME         STRING NOT NULL,
	COLUMN_NAME        STRING NOT NULL,
	ORDINAL_POSITION   INT NOT NULL,
	POSITION_IN_UNIQUE_CONSTRAINT INT
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDescWithTableLookup(ctx, p, dbContext, hideVirtual /*no constraints in virtual tables*/, func(
			db *sqlbase.DatabaseDescriptor,
			scName string,
			table *sqlbase.TableDescriptor,
			tableLookup tableLookupFn,
		) error {
			conInfo, err := table.GetConstraintInfoWithLookup(tableLookup.getTableByID)
			if err != nil {
				return err
			}
			dbNameStr := tree.NewDString(db.Name)
			scNameStr := tree.NewDString(scName)
			tbNameStr := tree.NewDString(table.Name)
			for conName, con := range conInfo {
				// Only Primary Key, Foreign Key, and Unique constraints are included.
				switch con.Kind {
				case sqlbase.ConstraintTypePK:
				case sqlbase.ConstraintTypeFK:
				case sqlbase.ConstraintTypeUnique:
				default:
					continue
				}

				cstNameStr := tree.NewDString(conName)

				for pos, col := range con.Columns {
					ordinalPos := tree.NewDInt(tree.DInt(pos + 1))
					uniquePos := tree.DNull
					if con.Kind == sqlbase.ConstraintTypeFK {
						uniquePos = ordinalPos
					}
					if err := addRow(
						dbNameStr,            // constraint_catalog
						scNameStr,            // constraint_schema
						cstNameStr,           // constraint_name
						dbNameStr,            // table_catalog
						scNameStr,            // table_schema
						tbNameStr,            // table_name
						tree.NewDString(col), // column_name
						ordinalPos,           // ordinal_position, 1-indexed
						uniquePos,            // position_in_unique_constraint
					); err != nil {
						return err
					}
				}
			}
			return nil
		})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-parameters.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/parameters-table.html
var informationSchemaParametersTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.parameters (
	SPECIFIC_CATALOG STRING,
	SPECIFIC_SCHEMA STRING,
	SPECIFIC_NAME STRING,
	ORDINAL_POSITION INT,
	PARAMETER_MODE STRING,
	IS_RESULT STRING,
	AS_LOCATOR STRING,
	PARAMETER_NAME STRING,
	DATA_TYPE STRING,
	CHARACTER_MAXIMUM_LENGTH INT,
	CHARACTER_OCTET_LENGTH INT,
	CHARACTER_SET_CATALOG STRING,
	CHARACTER_SET_SCHEMA STRING,
	CHARACTER_SET_NAME STRING,
	COLLATION_CATALOG STRING,
	COLLATION_SCHEMA STRING,
	COLLATION_NAME STRING,
	NUMERIC_PRECISION INT,
	NUMERIC_PRECISION_RADIX INT,
	NUMERIC_SCALE INT,
	DATETIME_PRECISION INT,
	INTERVAL_TYPE STRING,
	INTERVAL_PRECISION INT,
	UDT_CATALOG STRING,
	UDT_SCHEMA STRING,
	UDT_NAME STRING,
	SCOPE_CATALOG STRING,
	SCOPE_SCHEMA STRING,
	SCOPE_NAME STRING,
	MAXIMUM_CARDINALITY INT,
	DTD_IDENTIFIER STRING,
	PARAMETER_DEFAULT STRING
)`,
	// This is the same as information_schema.table_privileges. In postgres, this virtual table does
	// not show tables with grants provided through PUBLIC, but table_privileges does.
	// Since we don't have the PUBLIC concept, the two virtual tables are identical.
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return nil
	},
}

var (
	matchOptionFull    = tree.NewDString("FULL")
	matchOptionPartial = tree.NewDString("PARTIAL")
	matchOptionNone    = tree.NewDString("NONE")

	// Avoid unused warning for constants.
	_ = matchOptionPartial
	_ = matchOptionNone

	refConstraintRuleNoAction   = tree.NewDString("NO ACTION")
	refConstraintRuleRestrict   = tree.NewDString("RESTRICT")
	refConstraintRuleSetNull    = tree.NewDString("SET NULL")
	refConstraintRuleSetDefault = tree.NewDString("SET DEFAULT")
	refConstraintRuleCascade    = tree.NewDString("CASCADE")
)

func dStringForFKAction(action sqlbase.ForeignKeyReference_Action) tree.Datum {
	switch action {
	case sqlbase.ForeignKeyReference_NO_ACTION:
		return refConstraintRuleNoAction
	case sqlbase.ForeignKeyReference_RESTRICT:
		return refConstraintRuleRestrict
	case sqlbase.ForeignKeyReference_SET_NULL:
		return refConstraintRuleSetNull
	case sqlbase.ForeignKeyReference_SET_DEFAULT:
		return refConstraintRuleSetDefault
	case sqlbase.ForeignKeyReference_CASCADE:
		return refConstraintRuleCascade
	}
	panic(errors.Errorf("unexpected ForeignKeyReference_Action: %v", action))
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-referential-constraints.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/referential-constraints-table.html
var informationSchemaReferentialConstraintsTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.referential_constraints (
	CONSTRAINT_CATALOG        STRING NOT NULL,
	CONSTRAINT_SCHEMA         STRING NOT NULL,
	CONSTRAINT_NAME           STRING NOT NULL,
	UNIQUE_CONSTRAINT_CATALOG STRING NOT NULL,
	UNIQUE_CONSTRAINT_SCHEMA  STRING NOT NULL,
	UNIQUE_CONSTRAINT_NAME    STRING,
	MATCH_OPTION              STRING NOT NULL,
	UPDATE_RULE               STRING NOT NULL,
	DELETE_RULE               STRING NOT NULL,
	TABLE_NAME                STRING NOT NULL,
	REFERENCED_TABLE_NAME     STRING NOT NULL
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDescWithTableLookup(ctx, p, dbContext, hideVirtual /*no constraints in virtual tables*/, func(
			db *sqlbase.DatabaseDescriptor,
			scName string,
			table *sqlbase.TableDescriptor,
			tableLookup tableLookupFn,
		) error {
			dbNameStr := tree.NewDString(db.Name)
			scNameStr := tree.NewDString(scName)
			tbNameStr := tree.NewDString(table.Name)
			return forEachIndexInTable(table, func(index *sqlbase.IndexDescriptor) error {
				fk := index.ForeignKey
				if !fk.IsSet() {
					return nil
				}

				refTable, err := tableLookup.getTableByID(fk.Table)
				if err != nil {
					return err
				}
				refIndex, err := refTable.FindIndexByID(fk.Index)
				if err != nil {
					return err
				}

				return addRow(
					dbNameStr,                       // constraint_catalog
					scNameStr,                       // constraint_schema
					tree.NewDString(fk.Name),        // constraint_name
					dbNameStr,                       // unique_constraint_catalog
					scNameStr,                       // unique_constraint_schema
					tree.NewDString(refIndex.Name),  // unique_constraint_name
					matchOptionFull,                 // match_option
					dStringForFKAction(fk.OnUpdate), // update_rule
					dStringForFKAction(fk.OnDelete), // delete_rule
					tbNameStr,                       // table_name
					tree.NewDString(refTable.Name),  // referenced_table_name
				)
			})
		})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-role-table-grants.html
// MySQL:    missing
var informationSchemaRoleTableGrants = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.role_table_grants (
	GRANTOR        STRING,
	GRANTEE        STRING NOT NULL,
	TABLE_CATALOG  STRING NOT NULL,
	TABLE_SCHEMA   STRING NOT NULL,
	TABLE_NAME     STRING NOT NULL,
	PRIVILEGE_TYPE STRING NOT NULL,
	IS_GRANTABLE   STRING,
	WITH_HIERARCHY STRING
)`,
	// This is the same as information_schema.table_privileges. In postgres, this virtual table does
	// not show tables with grants provided through PUBLIC, but table_privileges does.
	// Since we don't have the PUBLIC concept, the two virtual tables are identical.
	populate: populateTablePrivileges,
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-routines.html
// MySQL:    https://dev.mysql.com/doc/mysql-infoschema-excerpt/5.7/en/routines-table.html
var informationSchemaRoutineTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.routines (
	SPECIFIC_CATALOG STRING,
	SPECIFIC_SCHEMA STRING,
	SPECIFIC_NAME STRING,
	ROUTINE_CATALOG STRING,
	ROUTINE_SCHEMA STRING,
	ROUTINE_NAME STRING,
	ROUTINE_TYPE STRING,
	MODULE_CATALOG STRING,
	MODULE_SCHEMA STRING,
	MODULE_NAME STRING,
	UDT_CATALOG STRING,
	UDT_SCHEMA STRING,
	UDT_NAME STRING,
	DATA_TYPE STRING,
	CHARACTER_MAXIMUM_LENGTH INT,
	CHARACTER_OCTET_LENGTH INT,
	CHARACTER_SET_CATALOG STRING,
	CHARACTER_SET_SCHEMA STRING,
	CHARACTER_SET_NAME STRING,
	COLLATION_CATALOG STRING,
	COLLATION_SCHEMA STRING,
	COLLATION_NAME STRING,
	NUMERIC_PRECISION INT,
	NUMERIC_PRECISION_RADIX INT,
	NUMERIC_SCALE INT,
	DATETIME_PRECISION INT,
	INTERVAL_TYPE STRING,
	INTERVAL_PRECISION STRING,
	TYPE_UDT_CATALOG STRING,
	TYPE_UDT_SCHEMA STRING,
	TYPE_UDT_NAME STRING,
	SCOPE_CATALOG STRING,
	SCOPE_NAME STRING,
	MAXIMUM_CARDINALITY INT,
	DTD_IDENTIFIER STRING,
	ROUTINE_BODY STRING,
	ROUTINE_DEFINITION STRING,
	EXTERNAL_NAME STRING,
	EXTERNAL_LANGUAGE STRING,
	PARAMETER_STYLE STRING,
	IS_DETERMINISTIC STRING,
	SQL_DATA_ACCESS STRING,
	IS_NULL_CALL STRING,
	SQL_PATH STRING,
	SCHEMA_LEVEL_ROUTINE STRING,
	MAX_DYNAMIC_RESULT_SETS INT,
	IS_USER_DEFINED_CAST STRING,
	IS_IMPLICITLY_INVOCABLE STRING,
	SECURITY_TYPE STRING,
	TO_SQL_SPECIFIC_CATALOG STRING,
	TO_SQL_SPECIFIC_SCHEMA STRING,
	TO_SQL_SPECIFIC_NAME STRING,
	AS_LOCATOR STRING,
	CREATED  TIMESTAMPTZ,
	LAST_ALTERED TIMESTAMPTZ,
	NEW_SAVEPOINT_LEVEL  STRING,
	IS_UDT_DEPENDENT STRING,
	RESULT_CAST_FROM_DATA_TYPE STRING,
	RESULT_CAST_AS_LOCATOR STRING,
	RESULT_CAST_CHAR_MAX_LENGTH  INT,
	RESULT_CAST_CHAR_OCTET_LENGTH STRING,
	RESULT_CAST_CHAR_SET_CATALOG STRING,
	RESULT_CAST_CHAR_SET_SCHEMA  STRING,
	RESULT_CAST_CHAR_SET_NAME STRING,
	RESULT_CAST_COLLATION_CATALOG STRING,
	RESULT_CAST_COLLATION_SCHEMA STRING,
	RESULT_CAST_COLLATION_NAME STRING,
	RESULT_CAST_NUMERIC_PRECISION INT,
	RESULT_CAST_NUMERIC_PRECISION_RADIX INT,
	RESULT_CAST_NUMERIC_SCALE INT,
	RESULT_CAST_DATETIME_PRECISION STRING,
	RESULT_CAST_INTERVAL_TYPE STRING,
	RESULT_CAST_INTERVAL_PRECISION INT,
	RESULT_CAST_TYPE_UDT_CATALOG STRING,
	RESULT_CAST_TYPE_UDT_SCHEMA  STRING,
	RESULT_CAST_TYPE_UDT_NAME STRING,
	RESULT_CAST_SCOPE_CATALOG STRING,
	RESULT_CAST_SCOPE_SCHEMA STRING,
	RESULT_CAST_SCOPE_NAME STRING,
	RESULT_CAST_MAXIMUM_CARDINALITY INT,
	RESULT_CAST_DTD_IDENTIFIER STRING
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return nil
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-schemata.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/schemata-table.html
var informationSchemaSchemataTable = virtualSchemaTable{
	schema: vtable.InformationSchemaSchemata,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachDatabaseDesc(ctx, p, dbContext, func(db *sqlbase.DatabaseDescriptor) error {
			return forEachSchemaName(ctx, p, db, func(sc string) error {
				return addRow(
					tree.NewDString(db.Name), // catalog_name
					tree.NewDString(sc),      // schema_name
					tree.DNull,               // default_character_set_name
					tree.DNull,               // sql_path
				)
			})
		})
	},
}

// Postgres: missing
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/schema-privileges-table.html
var informationSchemaSchemataTablePrivileges = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.schema_privileges (
	GRANTEE         STRING NOT NULL,
	TABLE_CATALOG   STRING NOT NULL,
	TABLE_SCHEMA    STRING NOT NULL,
	PRIVILEGE_TYPE  STRING NOT NULL,
	IS_GRANTABLE    STRING
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachDatabaseDesc(ctx, p, dbContext, func(db *sqlbase.DatabaseDescriptor) error {
			return forEachSchemaName(ctx, p, db, func(scName string) error {
				privs := db.Privileges.Show()
				dbNameStr := tree.NewDString(db.Name)
				scNameStr := tree.NewDString(scName)
				for _, u := range privs {
					userNameStr := tree.NewDString(u.User)
					for _, priv := range u.Privileges {
						if err := addRow(
							userNameStr,           // grantee
							dbNameStr,             // table_catalog
							scNameStr,             // table_schema
							tree.NewDString(priv), // privilege_type
							tree.DNull,            // is_grantable
						); err != nil {
							return err
						}
					}
				}
				return nil
			})
		})
	},
}

var (
	indexDirectionNA   = tree.NewDString("N/A")
	indexDirectionAsc  = tree.NewDString(sqlbase.IndexDescriptor_ASC.String())
	indexDirectionDesc = tree.NewDString(sqlbase.IndexDescriptor_DESC.String())
)

func dStringForIndexDirection(dir sqlbase.IndexDescriptor_Direction) tree.Datum {
	switch dir {
	case sqlbase.IndexDescriptor_ASC:
		return indexDirectionAsc
	case sqlbase.IndexDescriptor_DESC:
		return indexDirectionDesc
	}
	panic("unreachable")
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-sequences.html
// MySQL:    missing
var informationSchemaSequences = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.sequences (
    SEQUENCE_CATALOG         STRING NOT NULL,
    SEQUENCE_SCHEMA          STRING NOT NULL,
    SEQUENCE_NAME            STRING NOT NULL,
    DATA_TYPE                STRING NOT NULL,
    NUMERIC_PRECISION        INT NOT NULL,
    NUMERIC_PRECISION_RADIX  INT NOT NULL,
    NUMERIC_SCALE            INT NOT NULL,
    START_VALUE              STRING NOT NULL,
    MINIMUM_VALUE            STRING NOT NULL,
    MAXIMUM_VALUE            STRING NOT NULL,
    INCREMENT                STRING NOT NULL,
    CYCLE_OPTION             STRING NOT NULL
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDesc(ctx, p, dbContext, hideVirtual, /*no sequences in virtual schemas*/
			func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
				if !table.IsSequence() {
					return nil
				}
				return addRow(
					tree.NewDString(db.GetName()),    // catalog
					tree.NewDString(scName),          // schema
					tree.NewDString(table.GetName()), // name
					tree.NewDString("integer"),       // type
					tree.NewDInt(64),                 // numeric precision
					tree.NewDInt(2),                  // numeric precision radix
					tree.NewDInt(0),                  // numeric scale
					tree.NewDString(strconv.FormatInt(table.SequenceOpts.Start, 10)),     // start value
					tree.NewDString(strconv.FormatInt(table.SequenceOpts.MinValue, 10)),  // min value
					tree.NewDString(strconv.FormatInt(table.SequenceOpts.MaxValue, 10)),  // max value
					tree.NewDString(strconv.FormatInt(table.SequenceOpts.Increment, 10)), // increment
					noString, // cycle
				)
			})
	},
}

// Postgres: missing
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/statistics-table.html
var informationSchemaStatisticsTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.statistics (
	TABLE_CATALOG STRING NOT NULL,
	TABLE_SCHEMA  STRING NOT NULL,
	TABLE_NAME    STRING NOT NULL,
	NON_UNIQUE    STRING NOT NULL,
	INDEX_SCHEMA  STRING NOT NULL,
	INDEX_NAME    STRING NOT NULL,
	SEQ_IN_INDEX  INT NOT NULL,
	COLUMN_NAME   STRING NOT NULL,
	"COLLATION"   STRING,
	CARDINALITY   INT,
	DIRECTION     STRING NOT NULL,
	STORING       STRING NOT NULL,
	IMPLICIT      STRING NOT NULL
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDesc(ctx, p, dbContext, hideVirtual, /* virtual tables have no indexes*/
			func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
				dbNameStr := tree.NewDString(db.GetName())
				scNameStr := tree.NewDString(scName)
				tbNameStr := tree.NewDString(table.GetName())

				appendRow := func(index *sqlbase.IndexDescriptor, colName string, sequence int,
					direction tree.Datum, isStored, isImplicit bool,
				) error {
					return addRow(
						dbNameStr,                         // table_catalog
						scNameStr,                         // table_schema
						tbNameStr,                         // table_name
						yesOrNoDatum(!index.Unique),       // non_unique
						scNameStr,                         // index_schema
						tree.NewDString(index.Name),       // index_name
						tree.NewDInt(tree.DInt(sequence)), // seq_in_index
						tree.NewDString(colName),          // column_name
						tree.DNull,                        // collation
						tree.DNull,                        // cardinality
						direction,                         // direction
						yesOrNoDatum(isStored),            // storing
						yesOrNoDatum(isImplicit),          // implicit
					)
				}

				return forEachIndexInTable(table, func(index *sqlbase.IndexDescriptor) error {
					// Columns in the primary key that aren't in index.ColumnNames or
					// index.StoreColumnNames are implicit columns in the index.
					var implicitCols map[string]struct{}
					var hasImplicitCols bool
					if index.HasOldStoredColumns() {
						// Old STORING format: implicit columns are extra columns minus stored
						// columns.
						hasImplicitCols = len(index.ExtraColumnIDs) > len(index.StoreColumnNames)
					} else {
						// New STORING format: implicit columns are extra columns.
						hasImplicitCols = len(index.ExtraColumnIDs) > 0
					}
					if hasImplicitCols {
						implicitCols = make(map[string]struct{})
						for _, col := range table.PrimaryIndex.ColumnNames {
							implicitCols[col] = struct{}{}
						}
					}

					sequence := 1
					for i, col := range index.ColumnNames {
						// We add a row for each column of index.
						dir := dStringForIndexDirection(index.ColumnDirections[i])
						if err := appendRow(index, col, sequence, dir, false, false); err != nil {
							return err
						}
						sequence++
						delete(implicitCols, col)
					}
					for _, col := range index.StoreColumnNames {
						// We add a row for each stored column of index.
						if err := appendRow(index, col, sequence,
							indexDirectionNA, true, false); err != nil {
							return err
						}
						sequence++
						delete(implicitCols, col)
					}
					for col := range implicitCols {
						// We add a row for each implicit column of index.
						if err := appendRow(index, col, sequence,
							indexDirectionAsc, false, true); err != nil {
							return err
						}
						sequence++
					}
					return nil
				})
			})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-table-constraints.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/table-constraints-table.html
var informationSchemaTableConstraintTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.table_constraints (
	CONSTRAINT_CATALOG STRING NOT NULL,
	CONSTRAINT_SCHEMA  STRING NOT NULL,
	CONSTRAINT_NAME    STRING NOT NULL,
	TABLE_CATALOG      STRING NOT NULL,
	TABLE_SCHEMA       STRING NOT NULL,
	TABLE_NAME         STRING NOT NULL,
	CONSTRAINT_TYPE    STRING NOT NULL,
	IS_DEFERRABLE      STRING NOT NULL,
	INITIALLY_DEFERRED STRING NOT NULL
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDescWithTableLookup(ctx, p, dbContext, hideVirtual, /* virtual tables have no constraints */
			func(
				db *sqlbase.DatabaseDescriptor,
				scName string,
				table *sqlbase.TableDescriptor,
				tableLookup tableLookupFn,
			) error {
				conInfo, err := table.GetConstraintInfoWithLookup(tableLookup.getTableByID)
				if err != nil {
					return err
				}

				dbNameStr := tree.NewDString(db.Name)
				scNameStr := tree.NewDString(scName)
				tbNameStr := tree.NewDString(table.Name)

				for conName, c := range conInfo {
					if err := addRow(
						dbNameStr,                       // constraint_catalog
						scNameStr,                       // constraint_schema
						tree.NewDString(conName),        // constraint_name
						dbNameStr,                       // table_catalog
						scNameStr,                       // table_schema
						tbNameStr,                       // table_name
						tree.NewDString(string(c.Kind)), // constraint_type
						yesOrNoDatum(false),             // is_deferrable
						yesOrNoDatum(false),             // initially_deferred
					); err != nil {
						return err
					}
				}
				return nil
			})
	},
}

// Postgres: missing
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/user-privileges-table.html
var informationSchemaUserPrivileges = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.user_privileges (
	GRANTEE        STRING NOT NULL,
	TABLE_CATALOG  STRING NOT NULL,
	PRIVILEGE_TYPE STRING NOT NULL,
	IS_GRANTABLE   STRING
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachDatabaseDesc(ctx, p, dbContext, func(dbDesc *DatabaseDescriptor) error {
			dbNameStr := tree.NewDString(dbDesc.Name)
			for _, u := range []string{security.RootUser, sqlbase.AdminRole} {
				grantee := tree.NewDString(u)
				for _, p := range privilege.List(privilege.ByValue[:]).SortedNames() {
					if err := addRow(
						grantee,            // grantee
						dbNameStr,          // table_catalog
						tree.NewDString(p), // privilege_type
						tree.DNull,         // is_grantable
					); err != nil {
						return err
					}
				}
			}
			return nil
		})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-table-privileges.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/table-privileges-table.html
var informationSchemaTablePrivileges = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.table_privileges (
	GRANTOR        STRING,
	GRANTEE        STRING NOT NULL,
	TABLE_CATALOG  STRING NOT NULL,
	TABLE_SCHEMA   STRING NOT NULL,
	TABLE_NAME     STRING NOT NULL,
	PRIVILEGE_TYPE STRING NOT NULL,
	IS_GRANTABLE   STRING,
	WITH_HIERARCHY STRING
)`,
	populate: populateTablePrivileges,
}

// populateTablePrivileges is used to populate both table_privileges and role_table_grants.
func populateTablePrivileges(
	ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error,
) error {
	return forEachTableDesc(ctx, p, dbContext, virtualMany,
		func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
			dbNameStr := tree.NewDString(db.Name)
			scNameStr := tree.NewDString(scName)
			tbNameStr := tree.NewDString(table.Name)
			for _, u := range table.Privileges.Show() {
				for _, priv := range u.Privileges {
					if err := addRow(
						tree.DNull,              // grantor
						tree.NewDString(u.User), // grantee
						dbNameStr,               // table_catalog
						scNameStr,               // table_schema
						tbNameStr,               // table_name
						tree.NewDString(priv),   // privilege_type
						tree.DNull,              // is_grantable
						tree.DNull,              // with_hierarchy
					); err != nil {
						return err
					}
				}
			}
			return nil
		})
}

var (
	tableTypeSystemView = tree.NewDString("SYSTEM VIEW")
	tableTypeBaseTable  = tree.NewDString("BASE TABLE")
	tableTypeView       = tree.NewDString("VIEW")
)

var informationSchemaTablesTable = virtualSchemaTable{
	schema: vtable.InformationSchemaTables,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDesc(ctx, p, dbContext, virtualMany,
			func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
				if table.IsSequence() {
					return nil
				}
				tableType := tableTypeBaseTable
				insertable := yesString
				if isVirtualDescriptor(table) {
					tableType = tableTypeSystemView
					insertable = noString
				} else if table.IsView() {
					tableType = tableTypeView
					insertable = noString
				}
				dbNameStr := tree.NewDString(db.Name)
				scNameStr := tree.NewDString(scName)
				tbNameStr := tree.NewDString(table.Name)
				return addRow(
					dbNameStr,                              // table_catalog
					scNameStr,                              // table_schema
					tbNameStr,                              // table_name
					tableType,                              // table_type
					insertable,                             // is_insertable_into
					tree.NewDInt(tree.DInt(table.Version)), // version
				)
			})
	},
}

// Postgres: https://www.postgresql.org/docs/9.6/static/infoschema-views.html
// MySQL:    https://dev.mysql.com/doc/refman/5.7/en/views-table.html
var informationSchemaViewsTable = virtualSchemaTable{
	schema: `
CREATE TABLE information_schema.views (
    TABLE_CATALOG              STRING NOT NULL,
    TABLE_SCHEMA               STRING NOT NULL,
    TABLE_NAME                 STRING NOT NULL,
    VIEW_DEFINITION            STRING NOT NULL,
    CHECK_OPTION               STRING,
    IS_UPDATABLE               STRING,
    IS_INSERTABLE_INTO         STRING,
    IS_TRIGGER_UPDATABLE       STRING,
    IS_TRIGGER_DELETABLE       STRING,
    IS_TRIGGER_INSERTABLE_INTO STRING
)`,
	populate: func(ctx context.Context, p *planner, dbContext *DatabaseDescriptor, addRow func(...tree.Datum) error) error {
		return forEachTableDesc(ctx, p, dbContext, hideVirtual, /* virtual schemas have no views */
			func(db *sqlbase.DatabaseDescriptor, scName string, table *sqlbase.TableDescriptor) error {
				if !table.IsView() {
					return nil
				}
				// Note that the view query printed will not include any column aliases
				// specified outside the initial view query into the definition returned,
				// unlike Postgres. For example, for the view created via
				//  `CREATE VIEW (a) AS SELECT b FROM foo`
				// we'll only print `SELECT b FROM foo` as the view definition here,
				// while Postgres would more accurately print `SELECT b AS a FROM foo`.
				// TODO(a-robinson): Insert column aliases into view query once we
				// have a semantic query representation to work with (#10083).
				return addRow(
					tree.NewDString(db.Name),         // table_catalog
					tree.NewDString(scName),          // table_schema
					tree.NewDString(table.Name),      // table_name
					tree.NewDString(table.ViewQuery), // view_definition
					tree.DNull,                       // check_option
					tree.DNull,                       // is_updatable
					tree.DNull,                       // is_insertable_into
					tree.DNull,                       // is_trigger_updatable
					tree.DNull,                       // is_trigger_deletable
					tree.DNull,                       // is_trigger_insertable_into
				)
			})
	},
}

// forEachSchemaName iterates over the physical and virtual schemas.
func forEachSchemaName(
	ctx context.Context, p *planner, db *sqlbase.DatabaseDescriptor, fn func(string) error,
) error {
	scNames := []string{string(tree.PublicSchemaName)}
	// Handle virtual schemas.
	for _, schema := range p.getVirtualTabler().getEntries() {
		scNames = append(scNames, schema.desc.Name)
	}
	sort.Strings(scNames)
	for _, sc := range scNames {
		if err := fn(sc); err != nil {
			return err
		}
	}
	return nil
}

// forEachDatabaseDesc retrieves all database descriptors and iterates through them in
// lexicographical order with respect to their name. For each database, the function
// will call fn with its descriptor.
func forEachDatabaseDesc(
	ctx context.Context,
	p *planner,
	dbContext *DatabaseDescriptor,
	fn func(*sqlbase.DatabaseDescriptor) error,
) error {
	descs, err := p.Tables().getAllDescriptors(ctx, p.txn)
	if err != nil {
		return err
	}

	// Ignore table descriptors.
	var dbDescs []*sqlbase.DatabaseDescriptor
	for _, desc := range descs {
		if dbDesc, ok := desc.(*sqlbase.DatabaseDescriptor); ok &&
			(dbContext == nil || dbContext.ID == dbDesc.ID) &&
			userCanSeeDatabase(ctx, p, dbDesc) {
			dbDescs = append(dbDescs, dbDesc)
		}
	}

	for _, db := range dbDescs {
		if err := fn(db); err != nil {
			return err
		}
	}
	return nil
}

// forEachTableDesc retrieves all table descriptors from the current
// database and all system databases and iterates through them. For
// each table, the function will call fn with its respective database
// and table descriptor.
//
// The dbContext argument specifies in which database context we are
// requesting the descriptors. In context nil all descriptors are
// visible, in non-empty contexts only the descriptors of that
// database are visible.
//
// The virtualOpts argument specifies how virtual tables are made
// visible.
func forEachTableDesc(
	ctx context.Context,
	p *planner,
	dbContext *DatabaseDescriptor,
	virtualOpts virtualOpts,
	fn func(*sqlbase.DatabaseDescriptor, string, *sqlbase.TableDescriptor) error,
) error {
	return forEachTableDescWithTableLookup(ctx, p, dbContext, virtualOpts, func(
		db *sqlbase.DatabaseDescriptor,
		scName string,
		table *sqlbase.TableDescriptor,
		_ tableLookupFn,
	) error {
		return fn(db, scName, table)
	})
}

type virtualOpts int

const (
	// virtualMany iterates over virtual schemas in every catalog/database.
	virtualMany virtualOpts = iota
	// virtualOnce iterates over virtual schemas once, in the nil database.
	virtualOnce
	// hideVirtual completely hides virtual schemas during iteration.
	hideVirtual
)

// forEachTableDescAll does the same as forEachTableDesc but also
// includes newly added non-public descriptors.
func forEachTableDescAll(
	ctx context.Context,
	p *planner,
	dbContext *DatabaseDescriptor,
	virtualOpts virtualOpts,
	fn func(*sqlbase.DatabaseDescriptor, string, *sqlbase.TableDescriptor) error,
) error {
	return forEachTableDescWithTableLookupInternal(ctx,
		p, dbContext, virtualOpts, true, /* allowAdding */
		func(
			db *sqlbase.DatabaseDescriptor,
			scName string,
			table *sqlbase.TableDescriptor,
			_ tableLookupFn,
		) error {
			return fn(db, scName, table)
		})
}

// forEachTableDescWithTableLookup acts like forEachTableDesc, except it also provides a
// tableLookupFn when calling fn to allow callers to lookup fetched table descriptors
// on demand. This is important for callers dealing with objects like foreign keys, where
// the metadata for each object must be augmented by looking at the referenced table.
//
// The dbContext argument specifies in which database context we are
// requesting the descriptors.  In context "" all descriptors are
// visible, in non-empty contexts only the descriptors of that
// database are visible.
func forEachTableDescWithTableLookup(
	ctx context.Context,
	p *planner,
	dbContext *DatabaseDescriptor,
	virtualOpts virtualOpts,
	fn func(*sqlbase.DatabaseDescriptor, string, *sqlbase.TableDescriptor, tableLookupFn) error,
) error {
	return forEachTableDescWithTableLookupInternal(ctx, p, dbContext, virtualOpts, false /* allowAdding */, fn)
}

// forEachTableDescWithTableLookupInternal is the logic that supports
// forEachTableDescWithTableLookup.
//
// The allowAdding argument if true includes newly added tables that
// are not yet public.
func forEachTableDescWithTableLookupInternal(
	ctx context.Context,
	p *planner,
	dbContext *DatabaseDescriptor,
	virtualOpts virtualOpts,
	allowAdding bool,
	fn func(*DatabaseDescriptor, string, *TableDescriptor, tableLookupFn) error,
) error {
	descs, err := p.Tables().getAllDescriptors(ctx, p.txn)
	if err != nil {
		return err
	}
	lCtx := newInternalLookupCtx(descs, dbContext)

	if virtualOpts == virtualMany || virtualOpts == virtualOnce {
		// Virtual descriptors first.
		vt := p.getVirtualTabler()
		vEntries := vt.getEntries()
		vSchemaNames := vt.getSchemaNames()
		iterate := func(dbDesc *DatabaseDescriptor) error {
			for _, virtSchemaName := range vSchemaNames {
				e := vEntries[virtSchemaName]
				for _, tName := range e.orderedDefNames {
					te := e.defs[tName]
					if err := fn(dbDesc, virtSchemaName, te.desc, lCtx); err != nil {
						return err
					}
				}
			}
			return nil
		}

		switch virtualOpts {
		case virtualOnce:
			if err := iterate(nil); err != nil {
				return err
			}
		case virtualMany:
			for _, dbID := range lCtx.dbIDs {
				dbDesc := lCtx.dbDescs[dbID]
				if err := iterate(dbDesc); err != nil {
					return err
				}
			}
		}
	}

	// Physical descriptors next.
	for _, tbID := range lCtx.tbIDs {
		table := lCtx.tbDescs[tbID]
		dbDesc, parentExists := lCtx.dbDescs[table.GetParentID()]
		if table.Dropped() || !userCanSeeTable(ctx, p, table, allowAdding) || !parentExists {
			continue
		}
		if err := fn(dbDesc, tree.PublicSchema, table, lCtx); err != nil {
			return err
		}
	}
	return nil
}

func forEachIndexInTable(
	table *sqlbase.TableDescriptor, fn func(*sqlbase.IndexDescriptor) error,
) error {
	if table.IsPhysicalTable() {
		if err := fn(&table.PrimaryIndex); err != nil {
			return err
		}
	}
	for i := range table.Indexes {
		if err := fn(&table.Indexes[i]); err != nil {
			return err
		}
	}
	return nil
}

func forEachColumnInTable(
	table *sqlbase.TableDescriptor, fn func(*sqlbase.ColumnDescriptor) error,
) error {
	// Table descriptors already hold columns in-order.
	for i := range table.Columns {
		if err := fn(&table.Columns[i]); err != nil {
			return err
		}
	}
	return nil
}

func forEachColumnInIndex(
	table *sqlbase.TableDescriptor,
	index *sqlbase.IndexDescriptor,
	fn func(*sqlbase.ColumnDescriptor) error,
) error {
	colMap := make(map[sqlbase.ColumnID]*sqlbase.ColumnDescriptor, len(table.Columns))
	for i, column := range table.Columns {
		colMap[column.ID] = &table.Columns[i]
	}
	for _, columnID := range index.ColumnIDs {
		column := colMap[columnID]
		if err := fn(column); err != nil {
			return err
		}
	}
	return nil
}

func forEachRole(
	ctx context.Context, p *planner, fn func(username string, isRole bool) error,
) error {
	query := `SELECT username, "isRole" FROM system.users`
	rows, _ /* cols */, err := p.ExtendedEvalContext().ExecCfg.InternalExecutor.Query(
		ctx, "read-roles", p.txn, query,
	)
	if err != nil {
		return err
	}

	for _, row := range rows {
		username := tree.MustBeDString(row[0])
		isRole, ok := row[1].(*tree.DBool)
		if !ok {
			return errors.Errorf("isRole should be a boolean value, found %s instead", row[1].ResolvedType())
		}

		if err := fn(string(username), bool(*isRole)); err != nil {
			return err
		}
	}
	return nil
}

func forEachRoleMembership(
	ctx context.Context, p *planner, fn func(role, member string, isAdmin bool) error,
) error {
	query := `SELECT "role", "member", "isAdmin" FROM system.role_members`
	rows, _ /* cols */, err := p.ExtendedEvalContext().ExecCfg.InternalExecutor.Query(
		ctx, "read-members", p.txn, query,
	)
	if err != nil {
		return err
	}

	for _, row := range rows {
		roleName := tree.MustBeDString(row[0])
		memberName := tree.MustBeDString(row[1])
		isAdmin := row[2].(*tree.DBool)

		if err := fn(string(roleName), string(memberName), bool(*isAdmin)); err != nil {
			return err
		}
	}
	return nil
}

func userCanSeeDatabase(ctx context.Context, p *planner, db *sqlbase.DatabaseDescriptor) bool {
	return p.CheckAnyPrivilege(ctx, db) == nil
}

func userCanSeeTable(
	ctx context.Context, p *planner, table *sqlbase.TableDescriptor, allowAdding bool,
) bool {
	return tableIsVisible(table, allowAdding) && p.CheckAnyPrivilege(ctx, table) == nil
}

func tableIsVisible(table *TableDescriptor, allowAdding bool) bool {
	return table.State == sqlbase.TableDescriptor_PUBLIC ||
		(allowAdding && table.State == sqlbase.TableDescriptor_ADD)
}
