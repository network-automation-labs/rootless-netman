#!/bin/sh

systemctl daemon-reload

# Define the name of the group to be created
GROUP_NAME="rootless-netman"

# Check if the group already exists
if getent group "$GROUP_NAME" > /dev/null; then
    groupdel "$GROUP_NAME"
    if [ $? -eq 0 ]; then
        echo "Group '$GROUP_NAME' removed successfully."
    else
        echo "Error: Failed to remove group '$GROUP_NAME'."
        exit 1
    fi
fi

exit 0
