package pipeline

import (
	"errors"
	"fmt"
	"github.com/kubespace/kubespace/pkg/kube_resource"
	"github.com/kubespace/kubespace/pkg/model/types"
	"github.com/kubespace/kubespace/pkg/sse"
	"github.com/kubespace/kubespace/pkg/utils"
	"gorm.io/gorm"
	"k8s.io/klog"
	"strings"
	"time"
)

type ManagerPipelineRun struct {
	DB            *gorm.DB
	PluginManager *ManagerPipelinePlugin
	middleMessage *kube_resource.MiddleMessage
}

func NewPipelineRunManager(db *gorm.DB, pluginManager *ManagerPipelinePlugin, middleMessage *kube_resource.MiddleMessage) *ManagerPipelineRun {
	return &ManagerPipelineRun{DB: db, PluginManager: pluginManager, middleMessage: middleMessage}
}

func (p *ManagerPipelineRun) ListPipelineRun(pipelineId uint, lastBuildNumber int, status string, limit int) ([]types.PipelineRun, error) {
	var pipelineRuns []types.PipelineRun
	q := p.DB.Order("id desc").Limit(limit).Where("pipeline_id = ?", pipelineId)
	if lastBuildNumber != 0 {
		q = q.Where("build_number < ?", lastBuildNumber)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Find(&pipelineRuns).Error; err != nil {
		return nil, err
	}
	return pipelineRuns, nil
}

func (p *ManagerPipelineRun) GetLastPipelineRun(pipelineId uint) (*types.PipelineRun, error) {
	var lastPipelineRun types.PipelineRun
	if err := p.DB.Last(&lastPipelineRun, "pipeline_id = ?", pipelineId).Error; err != nil {
		if strings.Contains(err.Error(), "record not found") {
			return nil, nil
		}
		return nil, err
	}
	return &lastPipelineRun, nil
}

func (p *ManagerPipelineRun) GetLastBuildNumber(pipelineId uint) (uint, error) {
	var lastPipelineRun types.PipelineRun
	if err := p.DB.Last(&lastPipelineRun, "pipeline_id = ?", pipelineId).Error; err != nil {
		if strings.Contains(err.Error(), "record not found") {
			return 1, nil
		}
		return 0, err
	}
	return lastPipelineRun.BuildNumber + 1, nil
}

func (p *ManagerPipelineRun) CreatePipelineRun(pipelineRun *types.PipelineRun, stagesRun []*types.PipelineRunStage) (*types.PipelineRun, error) {
	err := p.DB.Transaction(func(tx *gorm.DB) error {
		var lastPipelineRun types.PipelineRun
		if err := tx.Set("gorm:query_option", "FOR UPDATE").Last(&lastPipelineRun, "pipeline_id = ?", pipelineRun.PipelineId).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		buildNum := lastPipelineRun.BuildNumber + 1
		pipelineRun.BuildNumber = buildNum
		pipelineRun.Env[types.PipelineEnvPipelineBuildNumber] = buildNum
		pipelineRun.Env[types.PipelineEnvPipelineTriggerUser] = pipelineRun.Operator
		if err := tx.Create(pipelineRun).Error; err != nil {
			return err
		}
		var prevStageRunId uint = 0
		for _, stageRun := range stagesRun {
			stageRun.PipelineRunId = pipelineRun.ID
			stageRun.PrevStageRunId = prevStageRunId
			if err := tx.Create(stageRun).Error; err != nil {
				return err
			}
			for _, jobRun := range stageRun.Jobs {
				jobRun.StageRunId = stageRun.ID
				if err := tx.Create(jobRun).Error; err != nil {
					return err
				}
			}
			prevStageRunId = stageRun.ID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pipelineRun, nil
}

func (p *ManagerPipelineRun) GetStageRunJobs(stageRunId uint) (types.PipelineRunJobs, error) {
	var runJobs []types.PipelineRunJob
	if err := p.DB.Where("stage_run_id = ?", stageRunId).Find(&runJobs).Error; err != nil {
		return nil, err
	}
	var stageRunJobs types.PipelineRunJobs
	for i, _ := range runJobs {
		stageRunJobs = append(stageRunJobs, &runJobs[i])
	}
	return stageRunJobs, nil
}

func (p *ManagerPipelineRun) NextStageRun(pipelineRunId uint, stageId uint) (*types.PipelineRunStage, error) {
	var err error
	var stageRun types.PipelineRunStage
	if err = p.DB.Last(&stageRun, "prev_stage_run_id = ? and pipeline_run_id = ?", stageId, pipelineRunId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if stageRun.Jobs, err = p.GetStageRunJobs(stageRun.ID); err != nil {
		return nil, err
	}
	return &stageRun, nil
}

func (p *ManagerPipelineRun) Get(pipelineRunId uint) (*types.PipelineRun, error) {
	var pipelineRun types.PipelineRun
	if err := p.DB.First(&pipelineRun, pipelineRunId).Error; err != nil {
		return nil, err
	}
	return &pipelineRun, nil
}

func (p *ManagerPipelineRun) GetJobRun(jobRunId uint) (*types.PipelineRunJob, error) {
	var jobRun types.PipelineRunJob
	if err := p.DB.First(&jobRun, jobRunId).Error; err != nil {
		return nil, err
	}
	return &jobRun, nil
}

func (p *ManagerPipelineRun) GetStageRun(stageId uint) (*types.PipelineRunStage, error) {
	var err error
	var stageRun types.PipelineRunStage
	if err = p.DB.First(&stageRun, stageId).Error; err != nil {
		return nil, err
	}
	if stageRun.Jobs, err = p.GetStageRunJobs(stageId); err != nil {
		return nil, err
	}
	return &stageRun, nil
}

func (p *ManagerPipelineRun) StagesRun(pipelineRunId uint) ([]*types.PipelineRunStage, error) {
	var stagesRun []types.PipelineRunStage
	if err := p.DB.Where("pipeline_run_id = ?", pipelineRunId).Find(&stagesRun).Error; err != nil {
		return nil, err
	}
	for i, stageRun := range stagesRun {
		stageRunJobs, err := p.GetStageRunJobs(stageRun.ID)
		if err != nil {
			return nil, err
		}
		stagesRun[i].Jobs = stageRunJobs
	}
	var seqStages []*types.PipelineRunStage
	prevStageId := uint(0)
	for {
		hasNext := false
		for i, s := range stagesRun {
			if s.PrevStageRunId == prevStageId {
				seqStages = append(seqStages, &stagesRun[i])
				prevStageId = s.ID
				hasNext = true
				break
			}
		}
		if !hasNext {
			break
		}
	}

	return seqStages, nil
}

func (p *ManagerPipelineRun) UpdateStageRun(stageRun *types.PipelineRunStage) error {
	err := p.DB.Transaction(func(tx *gorm.DB) error {
		if err := p.DB.Save(stageRun).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	var pipelineRun types.PipelineRun
	if err = p.DB.First(&pipelineRun, stageRun.PipelineRunId).Error; err != nil {
		return err
	}
	p.StreamPipelineRun(&pipelineRun)
	return err
}

func (p *ManagerPipelineRun) UpdateStageJobRunParams(stageRun *types.PipelineRunStage, jobRuns []*types.PipelineRunJob) error {
	return p.DB.Transaction(func(tx *gorm.DB) error {
		if err := p.DB.Select("custom_params").Save(stageRun).Error; err != nil {
			return err
		}
		for i, _ := range jobRuns {
			if err := p.DB.Select("params").Save(jobRuns[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetStageRunStatus 根据stage的所有任务的状态返回该stage的状态
// 1. 如果有doing的job，stage状态为doing；
// 2. 如果所有job的状态为error/ok/wait，则
// 	  a. job中有error的，则stage为error；
//    b. 所有job都为ok，则stage为ok；
//    c. job中有ok，有wait，则stage为doing；
func (p *ManagerPipelineRun) GetStageRunStatus(stageRun *types.PipelineRunStage) string {
	status := ""
	for _, jobRun := range stageRun.Jobs {
		if jobRun.Status == types.PipelineStatusDoing {
			return types.PipelineStatusDoing
		}
		if jobRun.Status == types.PipelineStatusError {
			status = types.PipelineStatusError
			continue
		}
		if jobRun.Status == types.PipelineStatusOK && status != types.PipelineStatusError {
			status = types.PipelineStatusOK
			continue
		}
		if jobRun.Status == types.PipelineStatusWait && status == types.PipelineStatusOK {
			status = types.PipelineStatusDoing
		}
	}
	if status == "" {
		status = stageRun.Status
	}
	return status
}

func (p *ManagerPipelineRun) GetStageRunEnv(stageRun *types.PipelineRunStage) types.Map {
	envs := make(map[string]interface{})
	for _, jobRun := range stageRun.Jobs {
		// 当前阶段所有Job合并env
		envs = utils.MergeMap(envs, jobRun.Env)
	}
	// 合并替换之前阶段的env
	stageEnvs := utils.MergeReplaceMap(stageRun.Env, envs)
	return stageEnvs
}

type UpdateStageObj struct {
	StageRunId     uint
	StageRunStatus string
	StageRunJobs   types.PipelineRunJobs
}

func (p *ManagerPipelineRun) UpdatePipelineStageRun(updateStageObj *UpdateStageObj) (*types.PipelineRun, *types.PipelineRunStage, error) {
	if updateStageObj == nil {
		klog.Info("parameter stageObj is empty")
		return nil, nil, errors.New("parameter is empty")
	}
	if updateStageObj.StageRunId == 0 {
		klog.Info("parameter stageRunId is empty")
		return nil, nil, errors.New("parameter stageRunId is empty")
	}
	var stageRun types.PipelineRunStage
	var pipelineRun types.PipelineRun
	err := p.DB.Transaction(func(tx *gorm.DB) error {
		var err error

		if err = tx.Set("gorm:query_option", "FOR UPDATE").First(&stageRun, updateStageObj.StageRunId).Error; err != nil {
			return err
		}

		if updateStageObj.StageRunJobs != nil {
			for _, runJob := range updateStageObj.StageRunJobs {
				if err := tx.Save(runJob).Error; err != nil {
					return err
				}
			}
		}

		if updateStageObj.StageRunStatus != "" {
			stageRun.Status = updateStageObj.StageRunStatus
		} else if updateStageObj.StageRunJobs != nil {
			var runJobs []types.PipelineRunJob
			if err = tx.Where("stage_run_id = ?", updateStageObj.StageRunId).Find(&runJobs).Error; err != nil {
				return err
			}
			stageRun.Jobs = types.PipelineRunJobs{}
			for i, _ := range runJobs {
				stageRun.Jobs = append(stageRun.Jobs, &runJobs[i])
			}
			stageRun.Status = p.GetStageRunStatus(&stageRun)
			stageEnvs := p.GetStageRunEnv(&stageRun)
			if stageEnvs != nil {
				stageRun.Env = stageEnvs
			}
		}
		if err = tx.First(&pipelineRun, stageRun.PipelineRunId).Error; err != nil {
			return err
		}
		if stageRun.Status == types.PipelineStatusError {
			pipelineRun.Status = types.PipelineStatusError
		} else if stageRun.Status == types.PipelineStatusDoing {
			pipelineRun.Status = types.PipelineStatusDoing
		} else if stageRun.Status == types.PipelineStatusOK {
			pipelineRun.Status = types.PipelineStatusDoing
		} else if stageRun.Status == types.PipelineStatusPause {
			pipelineRun.Status = types.PipelineStatusPause
		}
		now := time.Now()
		stageRun.UpdateTime = now
		pipelineRun.UpdateTime = now
		if err = tx.Save(&stageRun).Error; err != nil {
			return err
		}
		if err = tx.Save(&pipelineRun).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	p.StreamPipelineRun(&pipelineRun)
	return &pipelineRun, &stageRun, nil
}

func (p *ManagerPipelineRun) UpdatePipelineRun(pipelineRun *types.PipelineRun) error {
	pipelineRun.UpdateTime = time.Now()
	if err := p.DB.Save(pipelineRun).Error; err != nil {
		return err
	}
	p.StreamPipelineRun(pipelineRun)
	return nil
}

func (p *ManagerPipelineRun) StreamPipelineRun(pipelineRun *types.PipelineRun) {
	stageRuns, _ := p.StagesRun(pipelineRun.ID)
	event := sse.Event{
		Labels: map[string]string{
			sse.EventLabelType:       sse.EventTypePipelineRun,
			sse.EventTypePipelineRun: fmt.Sprintf("%d", pipelineRun.ID),
			sse.EventTypePipeline:    fmt.Sprintf("%d", pipelineRun.PipelineId),
		},
		Object: map[string]interface{}{
			"pipeline_run": pipelineRun,
			"stages_run":   stageRuns,
		},
	}
	p.middleMessage.SendGlobalWatch(event)
}

func (p *ManagerPipelineRun) GetEnvBeforeStageRun(stageRun *types.PipelineRunStage) (envs map[string]interface{}, err error) {
	if stageRun.PrevStageRunId == 0 {
		var pipelineRun types.PipelineRun
		if err = p.DB.Last(&pipelineRun, "id = ?", stageRun.PipelineRunId).Error; err != nil {
			return nil, err
		}
		envs = pipelineRun.Env
	} else {
		var prevStageRun types.PipelineRunStage
		if err = p.DB.Last(&prevStageRun, "id = ? and pipeline_run_id = ?", stageRun.PrevStageRunId, stageRun.PipelineRunId).Error; err != nil {
			return nil, err
		}
		envs = prevStageRun.Env
	}
	for k, v := range stageRun.CustomParams {
		envs[k] = v
	}
	return envs, nil
}

func (p *ManagerPipelineRun) GetJobRunLog(jobRunId uint, withLog bool) (*types.PipelineRunJobLog, error) {
	var jobLog types.PipelineRunJobLog
	if withLog {
		if err := p.DB.Last(&jobLog, "job_run_id = ?", jobRunId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
	} else {
		if err := p.DB.Select("id", "job_run_id", "create_time", "update_time").Last(&jobLog, "job_run_id = ?", jobRunId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
	}
	return &jobLog, nil
}
