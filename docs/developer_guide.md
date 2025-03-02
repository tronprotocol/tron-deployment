# Developer Guide

Welcome to the Developer Guide for the repository. This guide provides detailed instructions on how to contribute to the repository, including setting up the development environment, understanding the code structure, and following the contribution guidelines.

## Table of Contents

1. [Introduction](#introduction)
2. [Setting Up the Development Environment](#setting-up-the-development-environment)
3. [Code Structure](#code-structure)
4. [Contribution Guidelines](#contribution-guidelines)
5. [Testing](#testing)
6. [Additional Resources](#additional-resources)

## Introduction

This repository provides scripts and configuration files to help you deploy and manage a TRON network node. The scripts support both FullNode and SolidityNode deployments and include options for customizing the deployment based on your requirements.

## Setting Up the Development Environment

### Prerequisites

Before you begin, ensure that you have the following dependencies installed on your system:

- `java` (Oracle JDK, version >= 1.8)
- `git`
- `wget`
- `go` (for deploying the gRPC gateway)
- `protoc` (for deploying the gRPC gateway)

### Cloning the Repository

To clone the repository, run the following command:

```shell
git clone https://github.com/tronprotocol/TronDeployment.git
cd TronDeployment
```

### Installing Dependencies

Ensure that all required dependencies are installed on your system. You can use the following commands to install some of the dependencies:

```shell
# Install Java
sudo apt-get update
sudo apt-get install openjdk-8-jdk

# Install Git
sudo apt-get install git

# Install Wget
sudo apt-get install wget

# Install Go
sudo apt-get install golang

# Install Protoc
sudo apt-get install protobuf-compiler
```

## Code Structure

The repository is organized into several directories to help you navigate and understand the codebase:

- `scripts`: Contains the deployment scripts, such as `check-machine-config.sh`, `deploy_grpc_gateway.sh`, and `deploy_tron.sh`.
- `config`: Contains the configuration files, such as `main_net_config.conf`, `private_net_config.conf`, and `test_net_config.conf`.
- `docs`: Contains additional documentation files, such as the user guide, developer guide, and API documentation.

## Contribution Guidelines

We welcome contributions to the repository. To contribute, please follow these guidelines:

1. **Fork the Repository**: Fork the repository to your GitHub account by clicking the "Fork" button on the repository page.

2. **Create a Branch**: Create a new branch for your changes. Use a descriptive name for the branch, such as `feature/add-new-feature` or `bugfix/fix-issue`.

```shell
git checkout -b feature/add-new-feature
```

3. **Make Changes**: Make your changes to the codebase. Ensure that your changes are well-documented and follow the coding standards of the repository.

4. **Commit Changes**: Commit your changes with a descriptive commit message.

```shell
git add .
git commit -m "Add new feature to the repository"
```

5. **Push Changes**: Push your changes to your forked repository.

```shell
git push origin feature/add-new-feature
```

6. **Create a Pull Request**: Create a pull request to the main repository. Provide a detailed description of your changes and any relevant information.

## Testing

Before submitting your changes, ensure that your code is thoroughly tested. Follow these steps to run tests:

1. **Run Unit Tests**: Run the unit tests to ensure that your changes do not break any existing functionality.

```shell
# Navigate to the project directory
cd TronDeployment

# Run the tests
./gradlew test
```

2. **Test Deployment Scripts**: Test the deployment scripts to ensure that they work as expected. Follow the instructions in the user guide to deploy a FullNode or SolidityNode and verify that the deployment is successful.

## Additional Resources

For more information and resources, refer to the following:

- [TRON Protocol Documentation](https://developers.tron.network/docs)
- [TRON GitHub Repository](https://github.com/tronprotocol)
- [TRON Community Forum](https://forum.tron.network)
