package aliyun

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Snapshot is a snapshot of a server's system disk.
type Snapshot struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`    // PROGRESSING, ACCOMPLISHED or FAILED
	Progress int    `json:"progress"` // percent
	Created  string `json:"created"`  // RFC 3339
	DiskID   string `json:"diskId"`
	SizeGB   int    `json:"sizeGB,omitempty"` // ECS only
}

// SystemDisk finds a server's system disk, unless the listing already
// said which it is.
func (c *Client) SystemDisk(ctx context.Context, s Server) (string, error) {
	if s.SystemDiskID != "" {
		return s.SystemDiskID, nil
	}
	if s.Kind == KindECS {
		disks, err := c.ecsDisks(ctx, s.Region, s.ID)
		if err != nil {
			return "", err
		}
		if len(disks) > 0 {
			return disks[0].id, nil
		}
		return "", errors.New("找不到这台服务器的系统盘")
	}
	for page := 1; ; page++ {
		var out struct {
			Disks []struct {
				DiskID   string `json:"DiskId"`
				DiskType string `json:"DiskType"`
			} `json:"Disks"`
			TotalCount int `json:"TotalCount"`
		}
		err := c.Call(ctx, ProductSWAS, VersionSWAS, "ListDisks", s.Region,
			map[string]string{"InstanceId": s.ID, "PageSize": "100", "PageNumber": strconv.Itoa(page)}, &out)
		if err != nil {
			return "", err
		}
		for _, d := range out.Disks {
			if strings.EqualFold(d.DiskType, "system") {
				return d.DiskID, nil
			}
		}
		if len(out.Disks) == 0 || page*100 >= out.TotalCount {
			return "", errors.New("找不到这台服务器的系统盘")
		}
	}
}

// Snapshots lists the snapshots of a server's system disk.
func (c *Client) Snapshots(ctx context.Context, s Server) ([]Snapshot, error) {
	disk, err := c.SystemDisk(ctx, s)
	if err != nil {
		return nil, err
	}
	type snap struct {
		SnapshotID     string `json:"SnapshotId"`
		SnapshotName   string `json:"SnapshotName"`
		Status         string `json:"Status"`
		Progress       number `json:"Progress"` // "100%"
		CreationTime   string `json:"CreationTime"`
		SourceDiskID   string `json:"SourceDiskId"`
		SourceDiskSize number `json:"SourceDiskSize"`
	}
	convert := func(x snap) Snapshot {
		return Snapshot{ID: x.SnapshotID, Name: x.SnapshotName, State: strings.ToUpper(x.Status), Progress: int(x.Progress),
			Created: rfc3339(x.CreationTime), DiskID: x.SourceDiskID, SizeGB: int(x.SourceDiskSize)}
	}
	var list []Snapshot
	if s.Kind == KindSWAS {
		for page := 1; ; page++ {
			var out struct {
				Snapshots  []snap `json:"Snapshots"`
				TotalCount int    `json:"TotalCount"`
			}
			err := c.Call(ctx, ProductSWAS, VersionSWAS, "ListSnapshots", s.Region,
				map[string]string{"DiskId": disk, "PageSize": "100", "PageNumber": strconv.Itoa(page)}, &out)
			if err != nil {
				return list, err
			}
			for _, x := range out.Snapshots {
				list = append(list, convert(x))
			}
			if len(out.Snapshots) == 0 || len(list) >= out.TotalCount {
				return list, nil
			}
		}
	}
	for token := ""; ; {
		in := map[string]string{"DiskId": disk, "MaxResults": "100"}
		if token != "" {
			in["NextToken"] = token
		}
		var out struct {
			Snapshots struct {
				Snapshot []snap `json:"Snapshot"`
			} `json:"Snapshots"`
			NextToken string `json:"NextToken"`
		}
		if err := c.Call(ctx, ProductECS, VersionECS, "DescribeSnapshots", s.Region, in, &out); err != nil {
			return list, err
		}
		for _, x := range out.Snapshots.Snapshot {
			list = append(list, convert(x))
		}
		if out.NextToken == "" || out.NextToken == token || len(out.Snapshots.Snapshot) == 0 {
			return list, nil
		}
		token = out.NextToken
	}
}

// CreateSnapshot snapshots a server's system disk and returns the new
// snapshot's ID. An empty name becomes miao-<date>-<time>: names must
// start with a letter, and Simple Application Server takes only letters,
// digits and ":_.-", 2 to 50 of them.
func (c *Client) CreateSnapshot(ctx context.Context, s Server, name string) (string, error) {
	disk, err := c.SystemDisk(ctx, s)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = "miao-" + time.Now().Format("20060102-150405")
	}
	var out struct {
		SnapshotID string `json:"SnapshotId"`
	}
	in := map[string]string{"DiskId": disk, "SnapshotName": name}
	if s.Kind == KindSWAS {
		err = c.Call(ctx, ProductSWAS, VersionSWAS, "CreateSnapshot", s.Region, in, &out)
	} else {
		err = c.Call(ctx, ProductECS, VersionECS, "CreateSnapshot", s.Region, in, &out)
	}
	return out.SnapshotID, err
}

// ResetDisk rolls a server's system disk back to one of its snapshots
// (ResetDisk). The server has to be stopped; while the disk is being
// rolled back its status is ReIniting.
func (c *Client) ResetDisk(ctx context.Context, s Server, snapshotID string) error {
	disk, err := c.SystemDisk(ctx, s)
	if err != nil {
		return err
	}
	in := map[string]string{"DiskId": disk, "SnapshotId": snapshotID}
	if s.Kind == KindSWAS {
		return c.Call(ctx, ProductSWAS, VersionSWAS, "ResetDisk", s.Region, in, nil)
	}
	return c.Call(ctx, ProductECS, VersionECS, "ResetDisk", s.Region, in, nil)
}

// DiskStatus is the status of a server's system disk: In_use normally,
// ReIniting while it is rolled back.
func (c *Client) DiskStatus(ctx context.Context, s Server) (string, error) {
	if s.Kind == KindECS {
		disks, err := c.ecsDisks(ctx, s.Region, s.ID)
		if err != nil || len(disks) == 0 {
			return "", errors.Join(err, errors.New("找不到这台服务器的系统盘"))
		}
		return disks[0].status, nil
	}
	var out struct {
		Disks []struct {
			DiskType string `json:"DiskType"`
			Status   string `json:"Status"`
		} `json:"Disks"`
	}
	err := c.Call(ctx, ProductSWAS, VersionSWAS, "ListDisks", s.Region, map[string]string{"InstanceId": s.ID, "PageSize": "100"}, &out)
	if err != nil {
		return "", err
	}
	for _, d := range out.Disks {
		if strings.EqualFold(d.DiskType, "system") {
			return d.Status, nil
		}
	}
	return "", errors.New("找不到这台服务器的系统盘")
}
