# STEPS
// El sistema funciona siguiendo los siguientes pasos:
- El operador está en un bucle continuo de revisión, en el que comprueba la aparición de nodos con versión de kubernetes diferente.
- En cada bucle de iteración podrá descubrir:
a) una sola versión: implica que estamos en un momento de no-upgrade. 
- Se guarda en el estado del operador, indicando que está en un estado de no-upgrade.
- Condición de finalización: en cada bucle de reconciliación se analiza si ya están todos los workload migrados. (Entendiendo como tal que todos los pods de todos los workload están desplegados correctamente en nodos con versión de k8s actualiza, es decir, coincidentes con el target). Si se cumple lo anterior, volvemos al estado inicial, eliminando cualquier rastro dejado en el upgrade:
- Se pone estado de no-upgrade en el operador.
- Se elimina la información de target en el operador.
- Se quitan los taint referentes al upgrade en todos los nodos, y todos los tolerations existentes en los workload referentes a lo mismo.

b) dos versiones: implica que estamos en estado upgrade
- Se guarda en el estado del operador, indicando que está en estado upgrade.
- Se guarda el target, versión a la que queremos hacer el upgrade.
- Se actualizan los nodos:
   - Se hace una función en la que se recorren todos los nodos y se realizan dos funciones.
   1. Se marcan todos los nodos que tengan la versión target de k8s, con una label.
   2. Se marcan todos los nodos que tengan la versión target de k8s con un taint. (De este modo evitamos que planifique pods no deseados en nodos nuevos, hasta que les llegue el momento)
- Después de actualizar todos los nodos, se recorren todos los deploy y sts, y:
   1. se verifica cuales de ellos no tienen dependencias, o tiene sus dependencias cumplidas (entendiendo como tal, que todos los pods de los deploy de los que depende han sido migrados a nodos con versión k8s coincidente con la target).
   2. se añade los toleration hacia el nuevo taint, pero solo para todos los que no tienen dependencias.
   3. se crean o actualizan pbd:
       en los workload que tienen dependencias, si no tienen pdb se crea y se pone con disruption a zero, para no dejar hacer el rollout a ningún dependiente.
       en los workload que no tienen dependencias, se elimina el pdb para que sean migrados.





 