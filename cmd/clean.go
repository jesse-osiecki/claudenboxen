package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jesse-osiecki/claudenboxen/internal/image"
	"github.com/jesse-osiecki/claudenboxen/internal/vmimage"
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove cached base images other than the one currently in use",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, current, err := image.CurrentImage()
		if err != nil && err != image.ErrNoImage {
			return err
		}
		removed, err := image.PruneImages(current)
		if err != nil {
			return err
		}

		_, vmCurrent, err := vmimage.CurrentImage()
		if err != nil && err != vmimage.ErrNoImage {
			return err
		}
		vmRemoved, err := vmimage.PruneImages(vmCurrent)
		if err != nil {
			return err
		}

		if len(removed) == 0 && len(vmRemoved) == 0 {
			fmt.Println("nothing to clean")
			return nil
		}
		for _, hash := range removed {
			fmt.Println("removed", hash)
		}
		for _, hash := range vmRemoved {
			fmt.Println("removed VM image", hash)
		}
		return nil
	},
}
