package main

import (
	"fmt"
	"path/filepath"
	"strings"

	metadata "github.com/checkpoint-restore/checkpointctl/lib"
	"github.com/checkpoint-restore/go-criu/v7/crit"
	stats_pb "github.com/checkpoint-restore/go-criu/v7/crit/images/stats"
	spec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/xlab/treeprint"
)

func renderTreeView(tasks []task) error {
	for _, task := range tasks {
		info, err := getCheckpointInfo(task)
		if err != nil {
			return err
		}

		tree := buildTree(info.containerInfo, info.configDump, info.archiveSizes)

		if stats {
			dumpStats, err := crit.GetDumpStats(task.outputDir)
			if err != nil {
				return fmt.Errorf("failed to get dump statistics: %w", err)
			}

			addDumpStatsToTree(tree, dumpStats)
		}

		if psTree {
			c := crit.New(nil, nil, filepath.Join(task.outputDir, "checkpoint"), false, false)
			psTree, err := c.ExplorePs()
			if err != nil {
				return fmt.Errorf("failed to get process tree: %w", err)
			}

			if psTreeCmd {
				if err := updatePsTreeCommToCmdline(task.outputDir, psTree); err != nil {
					return fmt.Errorf("failed to process command line arguments: %w", err)
				}
			}

			if err = addPsTreeToTree(tree, psTree, task.outputDir); err != nil {
				return fmt.Errorf("failed to get process tree: %w", err)
			}
		}

		if files {
			c := crit.New(nil, nil, filepath.Join(task.outputDir, "checkpoint"), false, false)
			fds, err := c.ExploreFds()
			if err != nil {
				return fmt.Errorf("failed to get file descriptors: %w", err)
			}

			addFdsToTree(tree, fds)
		}

		if sockets {
			c := crit.New(nil, nil, filepath.Join(task.outputDir, "checkpoint"), false, false)
			fds, err := c.ExploreSk()
			if err != nil {
				return fmt.Errorf("failed to get sockets: %w", err)
			}

			addSkToTree(tree, fds)
		}

		if mounts {
			addMountsToTree(tree, info.specDump)
		}

		fmt.Printf("\nDisplaying container checkpoint tree view from %s\n\n", task.checkpointFilePath)
		fmt.Println(tree.String())
	}

	return nil
}

func buildTree(ci *containerInfo, containerConfig *metadata.ContainerConfig, archiveSizes *archiveSizes) treeprint.Tree {
	if ci.Name == "" {
		ci.Name = "Container"
	}
	tree := treeprint.NewWithRoot(ci.Name)

	tree.AddBranch(fmt.Sprintf("Image: %s", containerConfig.RootfsImageName))
	tree.AddBranch(fmt.Sprintf("ID: %s", containerConfig.ID))
	tree.AddBranch(fmt.Sprintf("Runtime: %s", containerConfig.OCIRuntime))
	tree.AddBranch(fmt.Sprintf("Created: %s", ci.Created))
	tree.AddBranch(fmt.Sprintf("Engine: %s", ci.Engine))

	if ci.IP != "" {
		tree.AddBranch(fmt.Sprintf("IP: %s", ci.IP))
	}
	if ci.MAC != "" {
		tree.AddBranch(fmt.Sprintf("MAC: %s", ci.MAC))
	}

	checkpointSize := tree.AddBranch(fmt.Sprintf("Checkpoint size: %s", metadata.ByteToString(archiveSizes.checkpointSize)))
	if archiveSizes.pagesSize != 0 {
		checkpointSize.AddNode(fmt.Sprintf("Memory pages size: %s", metadata.ByteToString(archiveSizes.pagesSize)))
	}
	if archiveSizes.amdgpuPagesSize != 0 {
		checkpointSize.AddNode(fmt.Sprintf("AMD GPU memory pages size: %s", metadata.ByteToString(archiveSizes.amdgpuPagesSize)))
	}

	if archiveSizes.rootFsDiffTarSize != 0 {
		tree.AddBranch(fmt.Sprintf("Root FS diff size: %s", metadata.ByteToString(archiveSizes.rootFsDiffTarSize)))
	}

	return tree
}

func addMountsToTree(tree treeprint.Tree, specDump *spec.Spec) {
	mountsTree := tree.AddBranch("Overview of mounts")
	for _, data := range specDump.Mounts {
		mountTree := mountsTree.AddBranch(fmt.Sprintf("Destination: %s", data.Destination))
		mountTree.AddBranch(fmt.Sprintf("Type: %s", data.Type))
		mountTree.AddBranch(fmt.Sprintf("Source: %s", func() string {
			return data.Source
		}()))
	}
}

func addDumpStatsToTree(tree treeprint.Tree, dumpStats *stats_pb.DumpStatsEntry) {
	statsTree := tree.AddBranch("CRIU dump statistics")
	statsTree.AddBranch(fmt.Sprintf("Freezing time: %s", FormatTime(dumpStats.GetFreezingTime())))
	statsTree.AddBranch(fmt.Sprintf("Frozen time: %s", FormatTime(dumpStats.GetFrozenTime())))
	statsTree.AddBranch(fmt.Sprintf("Memdump time: %s", FormatTime(dumpStats.GetMemdumpTime())))
	statsTree.AddBranch(fmt.Sprintf("Memwrite time: %s", FormatTime(dumpStats.GetMemwriteTime())))
	statsTree.AddBranch(fmt.Sprintf("Pages scanned: %d", dumpStats.GetPagesScanned()))
	statsTree.AddBranch(fmt.Sprintf("Pages written: %d", dumpStats.GetPagesWritten()))
}

func addPsTreeToTree(tree treeprint.Tree, psTree *crit.PsTree, checkpointOutputDir string) error {
	psRoot := psTree
	if pID != 0 {
		ps := psTree.FindPs(pID)
		if ps == nil {
			return fmt.Errorf("no process with PID %d (use `inspect --ps-tree` to view all PIDs)", pID)
		}
		psRoot = ps
	}

	// processNodes is a recursive function to create
	// a new branch for each process and add its child
	// processes as child nodes of the branch.
	var processNodes func(treeprint.Tree, *crit.PsTree) error
	processNodes = func(tree treeprint.Tree, root *crit.PsTree) error {
		node := tree.AddMetaBranch(root.PID, root.Comm)
		// attach environment variables to process
		if psTreeEnv {
			envVars, err := getPsEnvVars(checkpointOutputDir, root.PID)
			if err != nil {
				return err
			}

			nodeSubtree := node.AddBranch("Environment variables")
			for _, env := range envVars {
				nodeSubtree.AddBranch(env)
			}
		}
		for _, child := range root.Children {
			if err := processNodes(node, child); err != nil {
				return err
			}
		}
		return nil
	}
	psTreeNode := tree.AddBranch("Process tree")

	return processNodes(psTreeNode, psRoot)
}

func addFdsToTree(tree treeprint.Tree, fds []*crit.Fd) {
	var node treeprint.Tree
	for _, fd := range fds {
		node = tree.FindByMeta(fd.PId)
		// If FindByMeta returns nil, then the node with
		// the PID has been pruned while building the tree.
		// Hence, skip all associated file descriptors.
		if node == nil {
			continue
		}
		nodeSubtree := node.AddBranch("Open files")
		for _, file := range fd.Files {
			nodeSubtree.AddMetaBranch(strings.TrimSpace(file.Type+" "+file.Fd), file.Path)
		}
	}
}

func addSkToTree(tree treeprint.Tree, sks []*crit.Sk) {
	var node treeprint.Tree
	for _, sk := range sks {
		node = tree.FindByMeta(sk.PId)
		// If FindByMeta returns nil, then the node with
		// the PID has been pruned while building the tree.
		// Hence, skip all associated sockets.
		if node == nil {
			continue
		}

		nodeSubtree := node.AddBranch("Open sockets")
		var data string
		var protocol string
		for _, socket := range sk.Sockets {
			protocol = socket.Protocol
			switch socket.FdType {
			case "UNIXSK":
				// UNIX sockets do not have a protocol assigned.
				// Hence, the meta value for the socket is just
				// the socket type.
				protocol = fmt.Sprintf("UNIX (%s)", socket.Type)
				data = socket.SrcAddr
				if len(data) == 0 {
					// Use an abstract socket address
					data = "@"
				}
			case "INETSK":
				if protocol == "TCP" {
					protocol = fmt.Sprintf("%s (%s)", socket.Protocol, socket.State)
				}
				data = fmt.Sprintf(
					"%s:%d -> %s:%d (↑ %s ↓ %s)",
					socket.SrcAddr, socket.SrcPort,
					socket.DestAddr, socket.DestPort,
					socket.SendBuf, socket.RecvBuf,
				)
			case "PACKETSK":
				data = fmt.Sprintf("↑ %s ↓ %s", socket.SendBuf, socket.RecvBuf)
			case "NETLINKSK":
				data = fmt.Sprintf("↑ %s ↓ %s", socket.SendBuf, socket.RecvBuf)
			}

			nodeSubtree.AddMetaBranch(protocol, data)
		}
	}
}

// Recursively updates the Comm field of the psTree struct with the command line arguments
// from process memory pages
func updatePsTreeCommToCmdline(checkpointOutputDir string, psTree *crit.PsTree) error {
	cmdline, err := getCmdline(checkpointOutputDir, psTree.PID)
	if err != nil {
		return err
	}
	if cmdline != "" {
		psTree.Comm = cmdline
	}
	for _, child := range psTree.Children {
		if err := updatePsTreeCommToCmdline(checkpointOutputDir, child); err != nil {
			return err
		}
	}
	return nil
}
