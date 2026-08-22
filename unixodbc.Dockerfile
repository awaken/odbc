# Keep the integration toolchain aligned with the amd64 SQL Server image.
FROM --platform=linux/amd64 ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea

ARG DEBIAN_FRONTEND=noninteractive
ARG GO_VERSION=1.27.0
ARG GO_SHA256=675c26c449cbb18fc24b74650de1eabbae6e16f64326fd85a283fb3b58280685
ARG MICROSOFT_REPO_SHA256=c13f01ac7c3001b51a9281d40dde666db5e037e05512840c319832f7852bfec4
ARG MSODBCSQL_VERSION=18.6.2.1-1

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl git \
		freetds-bin freetds-dev tdsodbc unixodbc unixodbc-dev \
	&& rm -rf /var/lib/apt/lists/*

# Register Microsoft's scoped, signed Ubuntu 24.04 package repository.
RUN curl --fail --show-error --silent --location \
		--output /tmp/packages-microsoft-prod.deb \
		https://packages.microsoft.com/config/ubuntu/24.04/packages-microsoft-prod.deb \
	&& echo "${MICROSOFT_REPO_SHA256}  /tmp/packages-microsoft-prod.deb" | sha256sum --check --strict \
	&& dpkg -i /tmp/packages-microsoft-prod.deb \
	&& rm /tmp/packages-microsoft-prod.deb \
	&& apt-get update \
	&& ACCEPT_EULA=Y apt-get install -y --no-install-recommends "msodbcsql18=${MSODBCSQL_VERSION}" \
	&& rm -rf /var/lib/apt/lists/*

# Verify the Go archive against the SHA-256 digest published on go.dev.
RUN curl --fail --show-error --silent --location \
		--output /tmp/go.tar.gz \
		"https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
	&& echo "${GO_SHA256}  /tmp/go.tar.gz" | sha256sum --check --strict \
	&& tar -C /usr/local -xzf /tmp/go.tar.gz \
	&& rm /tmp/go.tar.gz

ENV PATH=/usr/local/go/bin:${PATH}

WORKDIR /src

#COPY mssqltest.sh /
#RUN chmod +x /mssqltest.sh
#ENTRYPOINT ["sh","/mssqltest.sh"]
#ENTRYPOINT ["sh"]
CMD ["sh"]
