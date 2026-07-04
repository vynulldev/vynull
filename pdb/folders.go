// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FolderNode represents a directory in the music folder tree.
type FolderNode struct {
	ID       uint32
	ParentID uint32
	Name     string
	IsFolder bool // true=contains subfolders, false=leaf playlist
	Children []*FolderNode
	TrackIDs []uint32 // tracks directly in this folder
}

// BuildFolderTree creates a folder/playlist tree from track file paths.
// musicRoot is the base directory (e.g., /path/to/music/).
// Returns the root node and a flat list of all nodes.
func BuildFolderTree(tracks []*Track, musicRoot string) (*FolderNode, []*FolderNode) {
	// Map relative directory paths to nodes.
	dirs := make(map[string]*FolderNode)
	var nextID uint32 = 1

	// Root node.
	root := &FolderNode{
		ID:       0,
		Name:     "ROOT",
		IsFolder: true,
	}

	for _, t := range tracks {
		// Get the path relative to musicRoot.
		relPath := t.FilePath
		if strings.HasPrefix(relPath, "/") {
			relPath = relPath[1:]
		}
		// Strip "Contents/" prefix if present (from USB layout).
		if strings.HasPrefix(relPath, "Contents/") {
			relPath = relPath[len("Contents/"):]
		}

		dir := filepath.Dir(relPath)
		if dir == "." {
			dir = ""
		}

		// Ensure all parent directories exist as nodes.
		ensureDir(dirs, root, dir, &nextID)

		// Add track to its directory node.
		if node, ok := dirs[dir]; ok {
			node.TrackIDs = append(node.TrackIDs, t.ID)
		} else {
			// Track in root.
			root.TrackIDs = append(root.TrackIDs, t.ID)
		}
	}

	// Determine which nodes are folders (have children) vs playlists (leaf).
	var allNodes []*FolderNode
	var walk func(n *FolderNode)
	walk = func(n *FolderNode) {
		n.IsFolder = len(n.Children) > 0
		if n.ID > 0 {
			allNodes = append(allNodes, n)
		}
		// Sort children by name.
		sort.Slice(n.Children, func(i, j int) bool {
			return n.Children[i].Name < n.Children[j].Name
		})
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)

	return root, allNodes
}

func ensureDir(dirs map[string]*FolderNode, root *FolderNode, dirPath string, nextID *uint32) {
	if dirPath == "" {
		return
	}
	if _, ok := dirs[dirPath]; ok {
		return
	}

	// Ensure parent exists first.
	parent := filepath.Dir(dirPath)
	if parent == "." {
		parent = ""
	}
	ensureDir(dirs, root, parent, nextID)

	// Create this directory node.
	node := &FolderNode{
		ID:   *nextID,
		Name: filepath.Base(dirPath),
	}
	*nextID++

	// Link to parent.
	if parent == "" {
		node.ParentID = 0
		root.Children = append(root.Children, node)
	} else {
		parentNode := dirs[parent]
		node.ParentID = parentNode.ID
		parentNode.Children = append(parentNode.Children, node)
	}

	dirs[dirPath] = node
}

// PlaylistTreeRow builds a PDB playlist_tree row for a folder node.
func PlaylistTreeRow(n *FolderNode, sortOrder int) []byte {
	nameBytes := encodeString(n.Name)
	isFolder := uint32(0)
	if n.IsFolder {
		isFolder = 1
	}

	// playlist_tree row: parent_id(4) + unknown(4) + sort_order(4) + id(4) + is_folder(4) + name
	row := make([]byte, 20+len(nameBytes))
	le32put(row, 0, n.ParentID)
	le32put(row, 4, 0)
	le32put(row, 8, uint32(sortOrder))
	le32put(row, 12, n.ID)
	le32put(row, 16, isFolder)
	copy(row[20:], nameBytes)
	return row
}

// PlaylistEntryRow builds a PDB playlist_entries row.
func PlaylistEntryRow(entryIndex, trackID, playlistID uint32) []byte {
	row := make([]byte, 12)
	le32put(row, 0, entryIndex)
	le32put(row, 4, trackID)
	le32put(row, 8, playlistID)
	return row
}

// WriteFolderTables adds playlist_tree and playlist_entries tables to
// PDB, with the tree derived from the filesystem layout under
// musicRoot. Shorthand for BuildFolderTree → WritePlaylistTablesFromNodes.
func WriteFolderTables(tracks []*Track, musicRoot string) (*tableBuilder, *tableBuilder) {
	_, allNodes := BuildFolderTree(tracks, musicRoot)
	return WritePlaylistTablesFromNodes(allNodes)
}

// WritePlaylistTablesFromNodes encodes a pre-built playlist tree into
// the PDB playlist_tree + playlist_entries tables. Use this when the
// tree comes from somewhere other than the filesystem (e.g. user
// playlists from PlaylistStore for a subset export).
func WritePlaylistTablesFromNodes(allNodes []*FolderNode) (*tableBuilder, *tableBuilder) {
	treeTable := newTableBuilder(TablePlaylistTree)
	entryTable := newTableBuilder(TablePlaylistEntries)

	for i, n := range allNodes {
		treeTable.addRow(PlaylistTreeRow(n, i))

		// Add track entries for leaf playlists.
		if !n.IsFolder {
			for j, trackID := range n.TrackIDs {
				entryTable.addRow(PlaylistEntryRow(uint32(j+1), trackID, n.ID))
			}
		}
		// Also add entries for folders that have direct tracks.
		if n.IsFolder && len(n.TrackIDs) > 0 {
			for j, trackID := range n.TrackIDs {
				entryTable.addRow(PlaylistEntryRow(uint32(j+1), trackID, n.ID))
			}
		}
	}

	return treeTable, entryTable
}

// FolderLookup provides runtime folder browsing from PDB tracks.
type FolderLookup struct {
	Root  *FolderNode
	Nodes map[uint32]*FolderNode // by ID
}

// BuildFolderLookup creates a folder lookup from PDB tracks.
func BuildFolderLookup(tracks []*Track) *FolderLookup {
	root, allNodes := BuildFolderTree(tracks, "")
	nodeMap := make(map[uint32]*FolderNode)
	for _, n := range allNodes {
		nodeMap[n.ID] = n
	}
	return &FolderLookup{Root: root, Nodes: nodeMap}
}

// ListDir returns the children (subfolders + tracks) of a folder by ID.
// folderID 0 = root.
func (fl *FolderLookup) ListDir(folderID uint32) []*FolderNode {
	if folderID == 0 {
		return fl.Root.Children
	}
	if n, ok := fl.Nodes[folderID]; ok {
		return n.Children
	}
	return nil
}

// TrackIDs returns tracks directly in a folder.
func (fl *FolderLookup) TrackIDs(folderID uint32) []uint32 {
	if folderID == 0 {
		return fl.Root.TrackIDs
	}
	if n, ok := fl.Nodes[folderID]; ok {
		return n.TrackIDs
	}
	return nil
}

// IsFolder returns whether a node has children.
func (fl *FolderLookup) IsFolder(id uint32) bool {
	if n, ok := fl.Nodes[id]; ok {
		return n.IsFolder
	}
	return false
}

// Used by os import.
var _ = os.PathSeparator
