task x {
  Int i
  String s
  Float f
  Array[Int] i
  Map[String, String] m

  command {
    echo ${i + 1} ${s} ${f + 3.2} ${join(i, ", ")} ${m["foo"]}
  }

  output {
    Int o1 = read_int(stdout())
    String o2 = read_string(stdout())
    Float o3 = 2.3
    Array[Int] o4 = [1,2,3]
    Map[String, Int] o5 = {"foo": 0, "bar": 1}
  }
}

workflow w {
  call x
}
