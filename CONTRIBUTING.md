# Contributing to Redis Operator

Thank you for considering contributing to Redis Operator We appreciate your interest in helping improve this project.

Whether you are fixing bugs, adding features, or improving documentation, your contributions are welcome. By following this guide, you'll help ensure that your contributions are accepted and make the process easier for everyone.

## How to Contribute

### 1. Create a Descriptive Issue

Open an issue using the provided templates:

- Bug report: use this template to report a bug. The issue must contain:
  - A detailed description of the problem
  - The expected behaviour
  - The proposed technical solution
- Feature request: suggest an idea/feature for this project. The issue must contain:
  - Feature motivation
  - Analysis of current status
  - The proposed technical solution

Once created, the project maintainers will review the issue and discuss the proposed solution. When the proposed solution is accepted, the developer can continue with the next step.


### 2. Fork the Repository

Begin by forking the repository. This will create a copy of the project in your GitHub account, which you can work on freely.

### 3. Clone Your Fork

Clone your fork to your local machine to work on it:

```bash
git clone https://github.com/your-username/[project-name].git
cd [project-name]
```

### 4. Create a Branch

Create a new branch for the changes you want to make:

```bash
git checkout -b your-branch-name
```

The name of the branch must be associated with the issue of the change, following the pattern:

```
<issue type>/GH-<issue number>-<description>
```

where:

- `<issue type>` is `bugfix` for a Bug report or `feature` for a Feature request.
- `<issue number>` is the issue number.
- `<description>` is a brief description (one or two words) of the issue.  


### 5. Make and Commit Your Changes

Make the necessary changes to the codebase, documentation, or any other aspect of the project. Be sure to:

- Write clear, concise commit messages.
- Follow the coding style and standards of the project.
- Use [conventional commits](https://www.conventionalcommits.org/).
- Ensure that your changes are well-documented and tested (if applicable).


### 6. Push Your Changes

Push your branch to your forked repository on GitHub:

```bash
git push origin your-branch-name
```

### 7. Create a Pull Request
Go to the original repository where you want to contribute and create a pull request (PR) from your branch. Be sure to include:

- A clear description of what your PR does.
- A link to any related issues or discussions.

We’ll review your PR and provide feedback. If everything looks good, we will merge it!


## Running tests

Redis Operator has both unit and behavioral (End To End) tests. Please refer to the [operator guide](./docs/operator-guide/toc.md), [developer guide](./docs/developer-guide.md) 
and [test guide](./test/README.md) for more information

