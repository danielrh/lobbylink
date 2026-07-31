# lobbylink C++ client: library + console chat example + interop test.
#
# Requires libdatachannel (https://github.com/paullouisageneau/libdatachannel)
# installed where find_package can see it (e.g. /usr/local). JSON is the
# vendored single-header nlohmann/json under third_party/ (MIT), a
# private implementation detail — it does not leak into the public
# headers or into consumers.

cmake_minimum_required(VERSION 3.16)
project(lobbylink-client-cpp CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(LibDataChannel REQUIRED)
find_package(Threads REQUIRED)

add_library(lobbylink src/lobbylink.cpp)
target_include_directories(lobbylink
    PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include
    PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/third_party)
target_link_libraries(lobbylink
    PUBLIC Threads::Threads
    PRIVATE LibDataChannel::LibDataChannel)

add_executable(chat examples/chat.cpp)
target_link_libraries(chat PRIVATE lobbylink)

add_executable(interop test/interop.cpp)
target_link_libraries(interop PRIVATE lobbylink)

# The interop test needs a running lobbylink server; point
# LOBBYLINK_SERVER at it (e.g. http://127.0.0.1:8787). Without it the
# test skips itself.
enable_testing()
add_test(NAME interop COMMAND interop)
set_tests_properties(interop PROPERTIES SKIP_RETURN_CODE 77)
