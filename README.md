# clix

Running CLIs via containers, and using container images as the distribution method.

`clix` makes it easy to "install" tools into your path, and takes care of downloading them and running them in whatever sandbox is available (Mac, Windows, Linux, inside a container like a kubernetes pod etc).

We support selective sandboxing, so (for example) the AWS CLI won't need access to your gcloud credentials, and the gcloud CLI won't need access to your AWS credentials. Only the mounts and environment variables declared in the script are visible to the tool (see the [sandbox contract](docs/design/sandboxing.md#sandbox-contract)).

## Contributing

This project is licensed under the [Apache 2.0 License](LICENSE).

We welcome contributions! Please see [docs/contributing.md](docs/contributing.md) for more information.

We follow [Google's Open Source Community Guidelines](https://opensource.google.com/conduct/).

## Disclaimer

This is not an officially supported Google product.

This project is not eligible for the Google Open Source Software Vulnerability Rewards Program.