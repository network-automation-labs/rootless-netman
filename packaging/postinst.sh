#!/bin/sh

# Define the name of the group to be created
GROUP_NAME="rootless-netman"

# Check if the group already exists
if getent group "$GROUP_NAME" > /dev/null; then
    echo "Group '$GROUP_NAME' already exists. Skipping creation."
else
    # Create the group
    # TODO: this is a debianism, find a more cross-distro way to do this
    addgroup --system "$GROUP_NAME"
    if [ $? -eq 0 ]; then
        echo "Group '$GROUP_NAME' created successfully."
    else
        echo "Error: Failed to create group '$GROUP_NAME'."
        exit 1
    fi
fi

systemctl daemon-reload
systemctl enable rootless-netman.socket rootless-netman.service
systemctl start rootless-netman.socket rootless-netman.service

exit 0
