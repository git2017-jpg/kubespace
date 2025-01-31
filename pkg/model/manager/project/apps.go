package project

import (
	"errors"
	"github.com/kubespace/kubespace/pkg/model/types"
	"gorm.io/gorm"
	"gorm.io/gorm/utils"
	"time"
)

type AppManager struct {
	*gorm.DB
	*AppVersionManager
}

func NewAppManager(chartManager *AppVersionManager, db *gorm.DB) *AppManager {
	return &AppManager{DB: db, AppVersionManager: chartManager}
}

func (a *AppManager) CreateProjectApp(chartFilePath string, app *types.ProjectApp, appVersion *types.AppVersion) (*types.ProjectApp, error) {
	var err error
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if app.ID == 0 {
			if err = tx.Create(app).Error; err != nil {
				return err
			}
		} else {
			if err = a.DB.Model(app).Select("update_user", "update_time", "description").Updates(*app).Error; err != nil {
				return err
			}
		}
		appVersion, err = a.AppVersionManager.CreateAppVersionWithChartPath(chartFilePath, types.AppVersionScopeProjectApp, app.ID, appVersion)
		if err != nil {
			return err
		}
		if app.Status == types.AppStatusUninstall {
			app.AppVersionId = appVersion.ID
			if err = tx.Save(app).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return app, nil
}

func (a *AppManager) GetByName(scope string, projectId uint, name string) (*types.ProjectApp, error) {
	var app types.ProjectApp
	err := a.DB.First(&app, "scope = ? and scope_id = ? and name = ?", scope, projectId, name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *AppManager) GetAppVersion(scope string, scopeId uint, packageName, packageVersion string) (*types.AppVersion, error) {
	var version types.AppVersion
	err := a.DB.First(&version, "scope = ? and scope_id = ? and package_name = ? and package_version = ?", scope, scopeId, packageName, packageVersion).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &version, nil
}

func (a *AppManager) ListProjectApps(scope string, scopeId uint) ([]*types.ProjectApp, error) {
	var apps []types.ProjectApp
	var err error
	if err = a.DB.Where("scope = ? and scope_id = ?", scope, scopeId).Find(&apps).Error; err != nil {
		return nil, err
	}
	var rets []*types.ProjectApp
	for i, app := range apps {
		apps[i].AppVersion, err = a.AppVersionManager.GetAppVersion(app.AppVersionId)
		if err != nil {
			return nil, err
		}
		rets = append(rets, &apps[i])
	}
	return rets, nil
}

func (a *AppManager) GetProjectApp(appId uint) (*types.ProjectApp, error) {
	var app types.ProjectApp
	var err error
	if err = a.DB.First(&app, "id = ?", appId).Error; err != nil {
		return nil, err
	}
	if app.AppVersion, err = a.AppVersionManager.GetAppVersion(app.AppVersionId); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *AppManager) GetProjectAppByName(scope string, scopeId uint, name string) (*types.ProjectApp, error) {
	var app types.ProjectApp
	var err error
	if err = a.DB.First(&app, "scope = ? and scope_id = ? and name = ?", scope, scopeId, name).Error; err != nil {
		return nil, err
	}
	if app.AppVersion, err = a.AppVersionManager.GetAppVersion(app.AppVersionId); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *AppManager) UpdateProjectApp(app *types.ProjectApp, columns ...string) error {
	if len(columns) == 0 {
		columns = []string{"*"}
	} else {
		if utils.Contains(columns, "update_time") {
			app.UpdateTime = time.Now()
			columns = append(columns, "update_time")
		}
	}
	if err := a.DB.Model(app).Select(columns).Updates(*app).Error; err != nil {
		return err
	}
	return nil
}

func (a *AppManager) DeleteProjectApp(appId uint) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		appVersions, err := a.ListAppVersions(types.AppVersionScopeProjectApp, appId)
		if err != nil {
			return err
		}
		for _, appVersion := range *appVersions {
			if err = a.AppVersionManager.DeleteVersion(appVersion.ID); err != nil {
				return err
			}
		}
		if err = tx.Delete(&types.ProjectAppRevision{}, "project_app_id=?", appId).Error; err != nil {
			return err
		}
		if err = tx.Delete(&types.ProjectApp{}, "id = ?", appId).Error; err != nil {
			return err
		}
		return nil
	})
}

func (a *AppManager) ImportApp(app *types.ProjectApp, appVersion *types.AppVersion) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if app.ID == 0 {
			if err := tx.Create(app).Error; err != nil {
				return err
			}
			appVersion.ScopeId = app.ID
		} else {
			app.UpdateTime = time.Now()
			if err := tx.Model(app).Select("update_user, update_time").Updates(*app).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(appVersion).Error; err != nil {
			return err
		}
		if app.Status == types.AppStatusUninstall {
			app.AppVersionId = appVersion.ID
			if err := tx.Model(app).Select("app_version_id").Updates(*app).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *AppManager) CreateRevision(appVersion *types.AppVersion, app *types.ProjectApp) (*types.ProjectAppRevision, error) {
	if app.Scope == types.AppVersionScopeProjectApp {
		var revision types.ProjectAppRevision
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			var lastRevisionNumber uint = 1
			var lastRevision types.ProjectAppRevision
			if err := tx.Last(&lastRevision, "project_app_id = ?", app.ID).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			} else {
				lastRevisionNumber = lastRevision.BuildRevision + 1
			}
			revision = types.ProjectAppRevision{
				ProjectAppId:  app.ID,
				BuildRevision: lastRevisionNumber,
				AppVersionId:  appVersion.ID,
				Values:        appVersion.Values,
				CreateUser:    app.UpdateUser,
				CreateTime:    time.Now(),
				UpdateTime:    time.Now(),
			}
			if err := tx.Create(&revision).Error; err != nil {
				return err
			}
			var cnt int64
			if err := tx.Model(&types.ProjectAppRevision{}).Where("project_app_id=?", app.ID).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt > 50 {
				if err := tx.Delete(&types.ProjectAppRevision{}, "project_app_id=? order by id limit ?", app.ID, cnt-50).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return &revision, nil
	}
	return nil, nil
}
