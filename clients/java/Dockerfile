# Build context: repository root.
FROM eclipse-temurin:21-jdk@sha256:23cc5734ff46e776f51ce66eb9ee08444fd47caabec0a95b7eb6e94be2d3696d AS build
ARG PGJDBC_VERSION=42.7.14
ARG PGJDBC_SHA256=73914527305a40cce504b0d3d90b23caf565136912d607ae8ae7c5895512332c
WORKDIR /src
RUN curl -fsSLo postgresql.jar https://repo1.maven.org/maven2/org/postgresql/postgresql/${PGJDBC_VERSION}/postgresql-${PGJDBC_VERSION}.jar \
 && echo "${PGJDBC_SHA256}  postgresql.jar" | sha256sum -c -
COPY clients/java/Client.java .
RUN javac -d classes Client.java

FROM eclipse-temurin:21-jre@sha256:227118764cccd3c3294617061aaa93a08309c63a395eefa83e6b35032992ea99
WORKDIR /app
COPY --from=build /src/postgresql.jar /app/postgresql.jar
COPY --from=build /src/classes /app/classes
COPY clients/markers.tsv /app/markers.tsv
USER 65532
ENTRYPOINT ["java", "-XX:MaxRAMPercentage=60", "-XX:+UseSerialGC", "-cp", "/app/classes:/app/postgresql.jar", "Client"]
